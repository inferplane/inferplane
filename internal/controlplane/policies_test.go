package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/policystore"
)

// fakeStore is an in-memory policystore.Store with injectable failures and a
// seed-call counter, so seeding-once and the commit-then-memory invariant
// are testable without a database.
type fakeStore struct {
	mu        sync.Mutex
	docs      map[string]policystore.Doc
	seeded    bool
	seedCalls int
	putErr    error
	listErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{docs: map[string]policystore.Doc{}}
}

func (f *fakeStore) List(context.Context) ([]policystore.Doc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]policystore.Doc, 0, len(f.docs))
	for _, d := range f.docs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeStore) Put(_ context.Context, name string, docYAML []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	f.docs[name] = policystore.Doc{Name: name, YAML: append([]byte(nil), docYAML...), UpdatedAt: time.Now()}
	return nil
}

func (f *fakeStore) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.docs[name]; !ok {
		return policystore.ErrNotFound
	}
	delete(f.docs, name)
	return nil
}

func (f *fakeStore) Seeded(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seeded, nil
}

func (f *fakeStore) Seed(_ context.Context, docs []policystore.Doc) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seedCalls++
	if f.seeded {
		return false, nil
	}
	for _, d := range docs {
		f.docs[d.Name] = policystore.Doc{Name: d.Name, YAML: append([]byte(nil), d.YAML...), UpdatedAt: time.Now()}
	}
	f.seeded = true
	return true, nil
}

var _ policystore.Store = (*fakeStore)(nil)

func TestAttachPolicyStoreSeedsExactlyOnce(t *testing.T) {
	s, _ := newTestServer(t, "")
	fs := newFakeStore()

	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatalf("AttachPolicyStore: %v", err)
	}
	if seeded, _ := fs.Seeded(context.Background()); !seeded {
		t.Fatal("store not seeded after attach")
	}
	if fs.seedCalls != 1 {
		t.Fatalf("seedCalls = %d, want 1", fs.seedCalls)
	}
	fs.mu.Lock()
	doc, ok := fs.docs["team-a"]
	n := len(fs.docs)
	fs.mu.Unlock()
	if !ok || n != 1 {
		t.Fatalf("seeded docs = %d, team-a present = %v; want exactly one doc named team-a", n, ok)
	}
	if !s.PolicyStoreAttached() {
		t.Fatal("PolicyStoreAttached must be true after attach")
	}

	// A second attach against the already-seeded store must not re-seed.
	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatalf("second AttachPolicyStore: %v", err)
	}
	if fs.seedCalls != 1 {
		t.Fatalf("seedCalls after second attach = %d, want 1 (marker-gated)", fs.seedCalls)
	}
	fs.mu.Lock()
	doc2 := fs.docs["team-a"]
	fs.mu.Unlock()
	if !bytes.Equal(doc.YAML, doc2.YAML) {
		t.Fatal("second attach replaced the stored document")
	}
}

func TestApplyWritePersistsAndHotApplies(t *testing.T) {
	s, ts := newTestServer(t, "")
	fs := newFakeStore()
	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	gen1 := s.generation
	s.mu.Unlock()

	edited := strings.Replace(cpPolicyYAML, "limitMilliUSD: 100", "limitMilliUSD: 200", 1)
	if err := s.ApplyWrite(context.Background(), "team-a", []byte(edited)); err != nil {
		t.Fatalf("ApplyWrite: %v", err)
	}

	fs.mu.Lock()
	stored := string(fs.docs["team-a"].YAML)
	fs.mu.Unlock()
	if stored != edited {
		t.Fatal("store does not hold the submitted body verbatim")
	}
	s.mu.Lock()
	gen2 := s.generation
	s.mu.Unlock()
	if gen2 == gen1 {
		t.Fatal("generation did not change after ApplyWrite")
	}

	// The export endpoint reflects the hot-applied set.
	resp, err := http.Get(ts.URL + "/v1alpha1/config/export")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "limitMilliUSD: 200") {
		t.Fatalf("export does not reflect the write:\n%s", body)
	}
}

// Spend reported against rule {team-a, cap} survives an ApplyWrite that
// keeps the rule name; renaming the rule resets its ledger row.
func TestApplyWriteLedgerCarryForward(t *testing.T) {
	s, ts := newTestServer(t, "")
	fs := newFakeStore()
	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatal(err)
	}

	doSync(t, ts.URL, "", policy.SyncRequest{
		Dataplane: "dp1",
		Reports:   []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 60_000}},
	})

	// Keep rule name "cap": spend carries forward. allowance = 60k + 10k.
	edited := strings.Replace(cpPolicyYAML, `allow: ["claude-haiku-4-5"]`, `allow: ["*"]`, 1)
	if err := s.ApplyWrite(context.Background(), "team-a", []byte(edited)); err != nil {
		t.Fatalf("ApplyWrite: %v", err)
	}
	r := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	if len(r.Leases) != 1 || r.Leases[0].AllowanceMicroUSD != 70_000 {
		t.Fatalf("leases after same-name write = %+v, want one lease with allowance 70000 (spend carried forward)", r.Leases)
	}

	// Rename the rule: its ledger state resets — fresh allowance = one grant.
	renamed := strings.Replace(edited, "name: cap", "name: cap2", 1)
	if err := s.ApplyWrite(context.Background(), "team-a", []byte(renamed)); err != nil {
		t.Fatalf("ApplyWrite (renamed rule): %v", err)
	}
	r = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	if len(r.Leases) != 1 || r.Leases[0].Rule != "cap2" || r.Leases[0].AllowanceMicroUSD != 10_000 {
		t.Fatalf("leases after rename = %+v, want one fresh lease for cap2 with allowance 10000", r.Leases)
	}
}

// Changing rule "cap"'s period in place (same policy+rule name) must NOT
// carry forward the old window's spend/allowance: a month's 60,000 µUSD
// booked against a $0.10 CalendarDay limit would misreport the team as
// already over budget for a window it never actually spent against.
func TestApplyWriteLedgerResetsOnPeriodChange(t *testing.T) {
	s, ts := newTestServer(t, "")
	fs := newFakeStore()
	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatal(err)
	}

	doSync(t, ts.URL, "", policy.SyncRequest{
		Dataplane: "dp1",
		Reports:   []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 60_000}},
	})

	// Same rule name, but now CalendarDay: the old month-scoped spend must
	// not survive into the new day-scoped ledger row.
	edited := strings.Replace(cpPolicyYAML, "limitMilliUSD: 100", "limitMilliUSD: 100\n      period: CalendarDay", 1)
	if err := s.ApplyWrite(context.Background(), "team-a", []byte(edited)); err != nil {
		t.Fatalf("ApplyWrite: %v", err)
	}
	r := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	if len(r.Leases) != 1 {
		t.Fatalf("leases = %+v, want exactly one", r.Leases)
	}
	if r.Leases[0].Period != v1alpha1.PeriodCalendarDay {
		t.Fatalf("lease period = %q, want %q", r.Leases[0].Period, v1alpha1.PeriodCalendarDay)
	}
	if r.Leases[0].AllowanceMicroUSD != 10_000 {
		t.Fatalf("allowance after period change = %d, want 10000 (fresh grant, old month spend NOT carried forward)", r.Leases[0].AllowanceMicroUSD)
	}
}

// A ConsumptionReport measured against a different period than the rule's
// CURRENT period (a stale/lagging data plane, or one heartbeat that landed
// right after an in-place period change) must not be booked into the
// ledger: the number is in the wrong currency for this window and would
// either falsely starve or falsely permit the new limit.
func TestSyncSkipsConsumptionReportWithStalePeriod(t *testing.T) {
	s, ts := newTestServer(t, "")
	fs := newFakeStore()
	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(cpPolicyYAML, "limitMilliUSD: 100", "limitMilliUSD: 100\n      period: CalendarDay", 1)
	if err := s.ApplyWrite(context.Background(), "team-a", []byte(edited)); err != nil {
		t.Fatalf("ApplyWrite: %v", err)
	}

	// This report omits Period, which the wire convention reads as
	// CalendarMonth (see ConsumptionReport.Period doc) — a mismatch against
	// the rule's now-CalendarDay ledger row.
	r := doSync(t, ts.URL, "", policy.SyncRequest{
		Dataplane: "dp1",
		Reports:   []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 60_000}},
	})
	if len(r.Leases) != 1 || r.Leases[0].AllowanceMicroUSD != 10_000 {
		t.Fatalf("leases = %+v, want allowance 10000 (stale-period report must be skipped, not booked as spend)", r.Leases)
	}
}

func TestApplyWriteValidationRejections(t *testing.T) {
	s, _ := newTestServer(t, "")
	fs := newFakeStore()
	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatal(err)
	}

	second := strings.Replace(cpPolicyYAML, "name: team-a", "name: team-b", 1)
	cases := map[string]struct {
		name string
		body string
	}{
		"name mismatch": {name: "other", body: cpPolicyYAML},
		"two documents": {name: "team-a", body: cpPolicyYAML + "---\n" + second},
		"invalid yaml":  {name: "team-a", body: "not: [valid"},
		"empty body":    {name: "team-a", body: ""},
	}
	for label, tc := range cases {
		err := s.ApplyWrite(context.Background(), tc.name, []byte(tc.body))
		if !errors.Is(err, ErrPolicyValidation) {
			t.Errorf("%s: err = %v, want ErrPolicyValidation", label, err)
		}
	}
}

// Commit-then-memory: a failed Put must leave the enforced set untouched.
func TestApplyWritePutFailureLeavesMemoryUntouched(t *testing.T) {
	s, _ := newTestServer(t, "")
	fs := newFakeStore()
	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	gen1 := s.generation
	wireLen := len(s.wire)
	s.mu.Unlock()

	fs.mu.Lock()
	fs.putErr = errors.New("boom")
	fs.mu.Unlock()

	edited := strings.Replace(cpPolicyYAML, "limitMilliUSD: 100", "limitMilliUSD: 200", 1)
	if err := s.ApplyWrite(context.Background(), "team-a", []byte(edited)); err == nil {
		t.Fatal("ApplyWrite must fail when Put fails")
	}
	s.mu.Lock()
	gen2 := s.generation
	wireLen2 := len(s.wire)
	s.mu.Unlock()
	if gen2 != gen1 || wireLen2 != wireLen {
		t.Fatal("a failed Put must leave s.wire/s.generation untouched")
	}
}

func TestApplyDelete(t *testing.T) {
	s, ts := newTestServer(t, "")
	fs := newFakeStore()
	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatal(err)
	}

	if err := s.ApplyDelete(context.Background(), "nope"); !errors.Is(err, policystore.ErrNotFound) {
		t.Fatalf("delete unknown: err = %v, want policystore.ErrNotFound", err)
	}

	if err := s.ApplyDelete(context.Background(), "team-a"); err != nil {
		t.Fatalf("ApplyDelete: %v", err)
	}
	s.mu.Lock()
	wireLen := len(s.wire)
	s.mu.Unlock()
	if wireLen != 0 {
		t.Fatalf("s.wire holds %d documents after delete, want 0", wireLen)
	}
	// Its ledger row is gone: the next sync grants no lease for {team-a, cap}.
	r := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	if len(r.Leases) != 0 {
		t.Fatalf("leases after delete = %+v, want none", r.Leases)
	}
}

func TestPolicyHTTPMapping(t *testing.T) {
	s, ts := newTestServer(t, "t")

	do := func(method, path, token, body string) *http.Response {
		t.Helper()
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, rdr)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// No bearer ⇒ 401.
	resp := do(http.MethodGet, "/v1alpha1/policies", "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET no bearer: status %d, want 401", resp.StatusCode)
	}

	// No store attached ⇒ writes 405, GET 200 with writable:false.
	resp = do(http.MethodPut, "/v1alpha1/policies/team-a", "t", cpPolicyYAML)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT no store: status %d, want 405", resp.StatusCode)
	}
	resp = do(http.MethodDelete, "/v1alpha1/policies/team-a", "t", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE no store: status %d, want 405", resp.StatusCode)
	}
	resp = do(http.MethodGet, "/v1alpha1/policies", "t", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET no store: status %d, want 200", resp.StatusCode)
	}
	var list struct {
		Generation string       `json:"generation"`
		Writable   bool         `json:"writable"`
		Policies   []policyView `json:"policies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if list.Writable {
		t.Fatal("GET no store: writable must be false")
	}
	if len(list.Policies) != 1 || list.Policies[0].Name != "team-a" {
		t.Fatalf("GET no store: policies = %+v, want one team-a", list.Policies)
	}

	// Attach a store: GET flips writable, PUT/DELETE go live.
	fs := newFakeStore()
	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatal(err)
	}
	resp = do(http.MethodGet, "/v1alpha1/policies", "t", "")
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !list.Writable {
		t.Fatal("GET with store: writable must be true")
	}

	resp = do(http.MethodPut, "/v1alpha1/policies/team-a", "t", cpPolicyYAML)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT happy: status %d, want 204", resp.StatusCode)
	}

	resp = do(http.MethodPut, "/v1alpha1/policies/wrong-name", "t", cpPolicyYAML)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT name mismatch: status %d, want 400", resp.StatusCode)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("PUT name mismatch: body is not valid JSON: %v", err)
	}
	resp.Body.Close()
	if errBody.Error == "" {
		t.Fatal("PUT name mismatch: JSON error field is empty")
	}

	resp = do(http.MethodDelete, "/v1alpha1/policies/nope", "t", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE unknown: status %d, want 404", resp.StatusCode)
	}

	// Empty set encodes policies as [], never null.
	resp = do(http.MethodDelete, "/v1alpha1/policies/team-a", "t", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE team-a: status %d, want 204", resp.StatusCode)
	}
	resp = do(http.MethodGet, "/v1alpha1/policies", "t", "")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), `"policies":[]`) {
		t.Fatalf("empty set must encode policies as []: %s", raw)
	}
}

// The mtime-watch guard: once a store is attached, file changes never
// trigger a reload over the DB-authoritative set.
func TestChangedFalseWithStoreAttached(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(f, []byte(cpPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer("", dir)
	if err != nil {
		t.Fatal(err)
	}
	// Bump the file mtime so the watch would fire.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(f, future, future); err != nil {
		t.Fatal(err)
	}
	if !s.changed() {
		t.Fatal("sanity: an mtime bump must report changed before a store is attached")
	}
	if err := s.AttachPolicyStore(context.Background(), newFakeStore()); err != nil {
		t.Fatal(err)
	}
	if s.changed() {
		t.Fatal("changed() must be false once a policy store is attached")
	}
}
