package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/policystore"
)

const importDocB = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-b }
spec:
  subject: { team: beta }
  rules:
  - name: models
    failurePolicy: FailOpen
    modelAccess: { allow: ["*"] }
`

// importServer is a write-enabled server with a fake store attached and
// seeded from cpPolicyYAML (one document, team-a).
func importServer(t *testing.T) (*Server, *httptest.Server, *fakeStore) {
	t.Helper()
	s, ts := newTestServerWrite(t, "t", "w")
	fs := newFakeStore()
	if err := s.AttachPolicyStore(context.Background(), fs); err != nil {
		t.Fatal(err)
	}
	return s, ts, fs
}

func currentGeneration(s *Server) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}

// doImport is the goroutine-safe request helper: it never calls t.Fatal.
func doImport(ts *httptest.Server, token, query, body string) (*http.Response, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1alpha1/config/import"+query, strings.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, b, err
}

// postImport is doImport for the test goroutine only.
func postImport(t *testing.T, ts *httptest.Server, token, query, body string) (*http.Response, []byte) {
	t.Helper()
	resp, b, err := doImport(ts, token, query, body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

func exportBody(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1alpha1/config/export", nil)
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="inferplane-policies-`) || !strings.HasSuffix(cd, `.yaml"`) {
		t.Fatalf("export Content-Disposition = %q", cd)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func decodeImport(t *testing.T, b []byte) ImportResult {
	t.Helper()
	var r ImportResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("decode import result: %v: %s", err, b)
	}
	return r
}

func TestImportRoundTripIsNoop(t *testing.T) {
	s, ts, fs := importServer(t)
	gen := currentGeneration(s)
	resp, b := postImport(t, ts, "w", "?dry_run=true", exportBody(t, ts))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dry run status %d: %s", resp.StatusCode, b)
	}
	r := decodeImport(t, b)
	if !r.DryRun || r.CurrentGeneration != gen || r.ResultGeneration != gen {
		t.Fatalf("round trip result %+v, want dry_run and both generations %s", r, gen)
	}
	if r.StoreRejected || strings.Contains(string(b), `"store_rejected"`) {
		t.Fatalf("round trip unexpectedly reported rejected stored rows: %s", b)
	}
	if r.Summary != (ImportSummary{Unchanged: 1}) || len(r.Changes) != 1 || r.Changes[0] != (ImportChange{Name: "team-a", Action: "unchanged"}) {
		t.Fatalf("round trip diff %+v", r)
	}
	if fs.replaceCalls != 0 {
		t.Fatal("dry run wrote to the store")
	}
}

func TestImportDiffIgnoresFormatting(t *testing.T) {
	_, ts, _ := importServer(t)
	// Same semantics as cpPolicyYAML, rewritten as JSON with reordered keys.
	reformatted := `{"kind":"GovernancePolicy","apiVersion":"inferplane.dev/v1alpha1","metadata":{"name":"team-a"},"spec":{"subject":{"team":"alpha"},"rules":[{"name":"cap","failurePolicy":"FailClosed","budget":{"hardCap":true,"limitMilliUSD":100,"lease":{"renewInterval":"5s","grantMilliUSD":10}}},{"name":"models","failurePolicy":"FailOpen","modelAccess":{"allow":["claude-haiku-4-5"]}}]}}`
	resp, b := postImport(t, ts, "w", "?dry_run=true", reformatted)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if r := decodeImport(t, b); r.Summary != (ImportSummary{Unchanged: 1}) {
		t.Fatalf("formatting-only change classified as %+v", r.Summary)
	}
}

func TestImportClassifiesAndAppliesReplace(t *testing.T) {
	s, ts, fs := importServer(t)
	gen := currentGeneration(s)
	bundle := importDocB // team-a absent ⇒ removed; team-b added
	resp, b := postImport(t, ts, "w", "?dry_run=true", bundle)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dry run %d: %s", resp.StatusCode, b)
	}
	preview := decodeImport(t, b)
	want := []ImportChange{{Name: "team-a", Action: "removed"}, {Name: "team-b", Action: "added"}}
	if len(preview.Changes) != 2 || preview.Changes[0] != want[0] || preview.Changes[1] != want[1] {
		t.Fatalf("changes %+v, want %+v (sorted by name)", preview.Changes, want)
	}
	if preview.Summary != (ImportSummary{Added: 1, Removed: 1}) {
		t.Fatalf("summary %+v", preview.Summary)
	}

	resp, b = postImport(t, ts, "w", "?expected_generation="+gen, bundle)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply %d: %s", resp.StatusCode, b)
	}
	applied := decodeImport(t, b)
	if applied.DryRun || applied.CurrentGeneration != gen {
		t.Fatalf("apply result %+v", applied)
	}
	if got := currentGeneration(s); got != preview.ResultGeneration || got != applied.ResultGeneration {
		t.Fatalf("installed generation %s; preview predicted %s, apply reported %s", got, preview.ResultGeneration, applied.ResultGeneration)
	}
	fs.mu.Lock()
	_, hasA := fs.docs["team-a"]
	_, hasB := fs.docs["team-b"]
	fs.mu.Unlock()
	if hasA || !hasB || fs.replaceCalls != 1 {
		t.Fatalf("store after apply: team-a=%v team-b=%v replaceCalls=%d", hasA, hasB, fs.replaceCalls)
	}
}

func TestImportValidationLeavesStoreUntouched(t *testing.T) {
	cases := map[string]string{
		"parse error":    "apiVersion: [unterminated",
		"missing name":   strings.Replace(importDocB, "metadata: { name: team-b }", "metadata: {}", 1),
		"duplicate name": importDocB + "---\n" + importDocB,
		"bad tier ref": cpPolicyYAML + `  - name: tiers
    failurePolicy: FailOpen
    routing:
      budgetTiers:
        budgetRef: no-such-rule
        tiers:
        - thresholdPercent: 80
          substitute: { claude-haiku-4-5: glm-4.7-gpu }
`,
	}
	for name, bundle := range cases {
		t.Run(name, func(t *testing.T) {
			s, ts, fs := importServer(t)
			gen := currentGeneration(s)
			for _, q := range []string{"?dry_run=true", "?expected_generation=" + gen} {
				resp, b := postImport(t, ts, "w", q, bundle)
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("%s: status %d, want 400: %s", q, resp.StatusCode, b)
				}
			}
			if fs.replaceCalls != 0 || currentGeneration(s) != gen {
				t.Fatal("rejected bundle reached the store or changed the generation")
			}
		})
	}
}

func TestImportIdentitySubjectViolationRejected(t *testing.T) {
	s, ts, fs := importServer(t)
	cfg := identity.Config{Organization: "acme", Required: true}
	if err := s.SetIdentityConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	bundle := strings.Replace(importDocB, "subject: { team: beta }", "subject: { team: beta, user: alice@example.com }", 1)
	resp, b := postImport(t, ts, "w", "?expected_generation="+currentGeneration(s), bundle)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", resp.StatusCode, b)
	}
	if fs.replaceCalls != 0 {
		t.Fatal("identity violation reached the store")
	}
}

func TestImportGenerationPrecondition(t *testing.T) {
	s, ts, fs := importServer(t)
	resp, _ := postImport(t, ts, "w", "", importDocB)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing expected_generation: status %d, want 400", resp.StatusCode)
	}
	resp, b := postImport(t, ts, "w", "?expected_generation=stale", importDocB)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale generation: status %d, want 409", resp.StatusCode)
	}
	var body map[string]string
	_ = json.Unmarshal(b, &body)
	if body["current_generation"] != currentGeneration(s) {
		t.Fatalf("409 body %s lacks current_generation", b)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("409 response is cacheable")
	}
	if fs.replaceCalls != 0 {
		t.Fatal("precondition failure reached the store")
	}
}

func TestImportEmptyBundleGuard(t *testing.T) {
	s, ts, fs := importServer(t)
	resp, b := postImport(t, ts, "w", "?dry_run=true", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty dry run %d: %s", resp.StatusCode, b)
	}
	if r := decodeImport(t, b); r.Summary != (ImportSummary{Removed: 1}) {
		t.Fatalf("empty dry run summary %+v", r.Summary)
	}
	gen := currentGeneration(s)
	if resp, _ := postImport(t, ts, "w", "?expected_generation="+gen, ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty apply without allow_empty: %d, want 400", resp.StatusCode)
	}
	if resp, b := postImport(t, ts, "w", "?allow_empty=true&expected_generation="+gen, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("empty apply with allow_empty: %d: %s", resp.StatusCode, b)
	}
	fs.mu.Lock()
	n := len(fs.docs)
	fs.mu.Unlock()
	if n != 0 {
		t.Fatalf("store holds %d docs after empty restore", n)
	}
}

func TestImportAuthStoreAndSize(t *testing.T) {
	// No store attached ⇒ 405 (file channel is read-only).
	_, bare := newTestServerWrite(t, "t", "w")
	if resp, _ := postImport(t, bare, "w", "?dry_run=true", importDocB); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("no store: %d, want 405", resp.StatusCode)
	}
	_, ts, _ := importServer(t)
	if resp, _ := postImport(t, ts, "", "?dry_run=true", importDocB); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d, want 401", resp.StatusCode)
	}
	if resp, _ := postImport(t, ts, "t", "?dry_run=true", importDocB); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("heartbeat token: %d, want 403", resp.StatusCode)
	}
	big := strings.Repeat("#", importMaxBodyBytes+1)
	if resp, _ := postImport(t, ts, "w", "?dry_run=true", big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: %d, want 413", resp.StatusCode)
	}
}

func TestImportStoreFailureLeavesStateUntouched(t *testing.T) {
	s, ts, fs := importServer(t)
	gen := currentGeneration(s)
	fs.mu.Lock()
	fs.replaceErr = errors.New("pgx: postgres://user:secret@db/x connection refused")
	fs.mu.Unlock()
	resp, b := postImport(t, ts, "w", "?expected_generation="+gen, importDocB)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	if strings.Contains(string(b), "secret") {
		t.Fatalf("store error leaked to caller: %s", b)
	}
	if currentGeneration(s) != gen {
		t.Fatal("failed Replace changed the in-memory generation")
	}
	fs.mu.Lock()
	_, hasA := fs.docs["team-a"]
	fs.mu.Unlock()
	if !hasA {
		t.Fatal("failed Replace dropped stored documents")
	}
}

func TestImportRecordsMutations(t *testing.T) {
	s, ts, _ := importServer(t)
	var mu sync.Mutex
	var got []MutationEntry
	s.SetMutationLog(func(e MutationEntry) { mu.Lock(); got = append(got, e); mu.Unlock() })
	// team-a unchanged (exported), team-b added.
	bundle := exportBody(t, ts) + "---\n" + importDocB
	resp, b := postImport(t, ts, "w", "?expected_generation="+currentGeneration(s), bundle)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply %d: %s", resp.StatusCode, b)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("mutation entries %+v, want put team-b + import", got)
	}
	if got[0].Op != "put" || got[0].Name != "team-b" || got[0].ContentHash == "" || got[0].Actor != "policy-write-token" {
		t.Fatalf("first entry %+v", got[0])
	}
	if got[1].Op != "import" || got[1].Name != "" || got[1].ContentHash == "" {
		t.Fatalf("import entry %+v", got[1])
	}
}

func TestImportSerializesWithApplyWrite(t *testing.T) {
	s, ts, fs := importServer(t)
	gen := currentGeneration(s)
	// Gate first, then its release cleanup: cleanups run LIFO, so the parked
	// handler is always released before newTestServerWrite's ts.Close (which
	// would otherwise wait forever on it). release is idempotent; never close
	// the gate any other way.
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)

	// Sequential: a PUT that lands first moves the generation, so an import
	// previewed against the old one is refused rather than overwriting it.
	edited := strings.Replace(cpPolicyYAML, "limitMilliUSD: 100 ", "limitMilliUSD: 200 ", 1)
	if err := s.ApplyWrite(context.Background(), "team-a", []byte(edited)); err != nil {
		t.Fatal(err)
	}
	if resp, _ := postImport(t, ts, "w", "?expected_generation="+gen, importDocB); resp.StatusCode != http.StatusConflict {
		t.Fatalf("import after PUT: %d, want 409", resp.StatusCode)
	}

	// Interleaved: park an import INSIDE Replace (after its generation
	// check). A concurrent ApplyWrite must not reach the store until the
	// import finishes — this fails if writeMu is removed, because the fake's
	// hooks fire before its own mutex.
	fs.replaceEntered = make(chan struct{}, 1)
	fs.replaceGate = gate
	fs.putStarted = make(chan struct{}, 1)
	type result struct {
		status int
		err    error
	}
	importDone := make(chan result, 1)
	go func() {
		resp, _, err := doImport(ts, "w", "?expected_generation="+currentGeneration(s), importDocB)
		if err != nil {
			importDone <- result{err: err}
			return
		}
		importDone <- result{status: resp.StatusCode}
	}()
	select {
	case <-fs.replaceEntered:
	case r := <-importDone:
		release()
		t.Fatalf("import finished before reaching Replace: status %d err %v", r.status, r.err)
	case <-time.After(5 * time.Second):
		release()
		t.Fatal("import never reached Replace")
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- s.ApplyWrite(context.Background(), "team-a", []byte(cpPolicyYAML)) }()
	select {
	case <-fs.putStarted:
		release()
		t.Fatal("ApplyWrite reached the store while an import was mid-commit (writeMu missing)")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	var r result
	select {
	case r = <-importDone:
	case <-time.After(5 * time.Second):
		t.Fatal("parked import never completed")
	}
	if r.err != nil || r.status != http.StatusOK {
		t.Fatalf("parked import: status %d err %v, want 200", r.status, r.err)
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("serialized ApplyWrite: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ApplyWrite never completed after the import released writeMu")
	}
	// The later writer (ApplyWrite) wins: team-a is back, team-b from the
	// import survives, and memory matches the store.
	fs.mu.Lock()
	_, hasA := fs.docs["team-a"]
	_, hasB := fs.docs["team-b"]
	fs.mu.Unlock()
	if !hasA || !hasB {
		t.Fatalf("final store team-a=%v team-b=%v, want both", hasA, hasB)
	}
	resp, b := postImport(t, ts, "w", "?dry_run=true", exportBody(t, ts))
	if r := decodeImport(t, b); resp.StatusCode != http.StatusOK || r.Summary != (ImportSummary{Unchanged: 2}) {
		t.Fatalf("memory and store diverged after serialized writers: %d %+v", resp.StatusCode, r.Summary)
	}
}

// A committed import is installed from the validated snapshot, never from
// a post-commit DB re-read: a List failure after commit cannot leave memory
// (and the expected_generation check) on the old set.
func TestImportInstallsCommittedSetWithoutReread(t *testing.T) {
	s, ts, fs := importServer(t)
	gen := currentGeneration(s)
	resp, b := postImport(t, ts, "w", "?dry_run=true", importDocB)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dry run %d: %s", resp.StatusCode, b)
	}
	preview := decodeImport(t, b)
	fs.mu.Lock()
	fs.listErrAfterReplace = errors.New("db unreachable after commit") // the pre-apply reconcile still succeeds
	fs.mu.Unlock()
	resp, b = postImport(t, ts, "w", "?expected_generation="+gen, importDocB)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply with failing List: %d: %s", resp.StatusCode, b)
	}
	if got := currentGeneration(s); got != preview.ResultGeneration {
		t.Fatalf("installed generation %s, want previewed %s", got, preview.ResultGeneration)
	}
}

// A PUT that committed but failed to reload leaves memory stale; the import
// reconciles with the store first, so a preview taken before that PUT is
// refused instead of silently overwriting it.
func TestImportReconcilesStaleMemoryBeforePrecondition(t *testing.T) {
	s, ts, fs := importServer(t)
	gen := currentGeneration(s)
	fs.mu.Lock()
	fs.listErr = errors.New("transient read failure")
	fs.mu.Unlock()
	edited := strings.Replace(cpPolicyYAML, "limitMilliUSD: 100 ", "limitMilliUSD: 300 ", 1)
	if err := s.ApplyWrite(context.Background(), "team-a", []byte(edited)); err == nil {
		t.Fatal("ApplyWrite with failing reload unexpectedly succeeded")
	}
	if currentGeneration(s) != gen {
		t.Fatal("precondition for this test: memory should still be stale")
	}
	// While the store is unreadable, import refuses (503) rather than judge
	// against stale memory.
	if resp, _ := postImport(t, ts, "w", "?expected_generation="+gen, importDocB); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("import with unreadable store: %d, want 503", resp.StatusCode)
	}
	fs.mu.Lock()
	fs.listErr = nil
	fs.mu.Unlock()
	if resp, _ := postImport(t, ts, "w", "?expected_generation="+gen, importDocB); resp.StatusCode != http.StatusConflict {
		t.Fatalf("import against the pre-PUT generation: %d, want 409", resp.StatusCode)
	}
}

func TestImportDiffUsesStoredRowsWhenStoreSetIsRejected(t *testing.T) {
	s, ts, fs := importServer(t)
	gen := currentGeneration(s)
	fs.mu.Lock()
	fs.docs["broken"] = policystore.Doc{Name: "broken", YAML: []byte("not: [valid")}
	fs.mu.Unlock()
	var mu sync.Mutex
	var got []MutationEntry
	s.SetMutationLog(func(e MutationEntry) { mu.Lock(); got = append(got, e); mu.Unlock() })

	resp, b := postImport(t, ts, "w", "?dry_run=true", importDocB)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dry run %d: %s", resp.StatusCode, b)
	}
	preview := decodeImport(t, b)
	want := []ImportChange{{Name: "broken", Action: "removed"}, {Name: "team-a", Action: "removed"}, {Name: "team-b", Action: "added"}}
	if preview.CurrentGeneration != gen || preview.Summary != (ImportSummary{Added: 1, Removed: 2}) {
		t.Fatalf("rejected store preview %+v, want current generation %s and two removals", preview, gen)
	}
	if len(preview.Changes) != len(want) {
		t.Fatalf("changes %+v, want %+v", preview.Changes, want)
	}
	for i := range want {
		if preview.Changes[i] != want[i] {
			t.Fatalf("changes %+v, want %+v", preview.Changes, want)
		}
	}
	resp, b = postImport(t, ts, "w", "?expected_generation="+gen, importDocB)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply %d: %s", resp.StatusCode, b)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range got {
		if e.Op == "delete" && e.Name == "broken" {
			return
		}
	}
	t.Fatalf("mutation entries %+v lack delete broken", got)
}

func TestImportRejectedStoreBranchCanonicalizesValidRows(t *testing.T) {
	s, ts, fs := importServer(t)
	gen := currentGeneration(s)
	// Same semantics as cpPolicyYAML, stored as JSON with reordered keys.
	reformatted := `{"kind":"GovernancePolicy","apiVersion":"inferplane.dev/v1alpha1","metadata":{"name":"team-a"},"spec":{"subject":{"team":"alpha"},"rules":[{"name":"cap","failurePolicy":"FailClosed","budget":{"hardCap":true,"limitMilliUSD":100,"lease":{"renewInterval":"5s","grantMilliUSD":10}}},{"name":"models","failurePolicy":"FailOpen","modelAccess":{"allow":["claude-haiku-4-5"]}}]}}`
	fs.mu.Lock()
	fs.docs["team-a"] = policystore.Doc{Name: "team-a", YAML: []byte(reformatted)}
	fs.docs["broken"] = policystore.Doc{Name: "broken", YAML: []byte("not: [valid")}
	fs.mu.Unlock()
	var mu sync.Mutex
	var got []MutationEntry
	s.SetMutationLog(func(e MutationEntry) { mu.Lock(); got = append(got, e); mu.Unlock() })

	resp, b := postImport(t, ts, "w", "?dry_run=true", cpPolicyYAML)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dry run %d: %s", resp.StatusCode, b)
	}
	preview := decodeImport(t, b)
	want := []ImportChange{{Name: "broken", Action: "removed"}, {Name: "team-a", Action: "unchanged"}}
	if !preview.StoreRejected || preview.CurrentGeneration != gen || preview.ResultGeneration != gen || preview.Summary != (ImportSummary{Removed: 1, Unchanged: 1}) {
		t.Fatalf("rejected store preview %+v, want generation %s, one removal and one unchanged", preview, gen)
	}
	if len(preview.Changes) != len(want) {
		t.Fatalf("changes %+v, want %+v", preview.Changes, want)
	}
	for i := range want {
		if preview.Changes[i] != want[i] {
			t.Fatalf("changes %+v, want %+v", preview.Changes, want)
		}
	}
	if fs.replaceCalls != 0 {
		t.Fatal("dry run wrote to the store")
	}
	mu.Lock()
	mutations := len(got)
	mu.Unlock()
	if mutations != 0 {
		t.Fatal("dry run recorded mutations")
	}

	resp, b = postImport(t, ts, "w", "?expected_generation="+preview.CurrentGeneration, cpPolicyYAML)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply %d: %s", resp.StatusCode, b)
	}
	if applied := decodeImport(t, b); !applied.StoreRejected || applied.Summary != preview.Summary {
		t.Fatalf("applied result %+v, want rejected store and preview summary %+v", applied, preview.Summary)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Op != "delete" || got[0].Name != "broken" || got[1].Op != "import" || got[1].Name != "" {
		t.Fatalf("mutation entries %+v, want delete broken + import only", got)
	}
}

// A lost commit acknowledgement is resolved by reading back: the store holds
// exactly the predicted set, so the restore reports success and is audited.
func TestImportLostCommitAckIsConfirmedByReadBack(t *testing.T) {
	s, ts, fs := importServer(t)
	var mu sync.Mutex
	var ops []string
	s.SetMutationLog(func(e MutationEntry) { mu.Lock(); ops = append(ops, e.Op); mu.Unlock() })
	fs.mu.Lock()
	fs.replaceCommitThenErr = errors.New("connection reset during COMMIT")
	fs.mu.Unlock()
	resp, b := postImport(t, ts, "w", "?expected_generation="+currentGeneration(s), importDocB)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lost-ack import: %d: %s", resp.StatusCode, b)
	}
	if got := decodeImport(t, b).ResultGeneration; got != currentGeneration(s) {
		t.Fatalf("reported %s, installed %s", got, currentGeneration(s))
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(ops, ",") != "delete,put,import" {
		t.Fatalf("committed restore audit %v, want delete(team-a),put(team-b),import", ops)
	}
}

// The fleet bound covers the full sync response, even when the YAML bundle
// fits the transport cap.
func TestImportRefusesUndistributableSet(t *testing.T) {
	_, ts, fs := importServer(t)
	prev := importMaxSyncBytes
	importMaxSyncBytes = 256
	t.Cleanup(func() { importMaxSyncBytes = prev })
	resp, b := postImport(t, ts, "w", "?dry_run=true", cpPolicyYAML+"---\n"+importDocB)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "worst-case sync") {
		t.Fatalf("oversized set: %d %s, want 400 naming the worst-case sync bound", resp.StatusCode, b)
	}
	if fs.replaceCalls != 0 {
		t.Fatal("undistributable set reached the store")
	}
}

func TestImportRefusesRepeatedLongIdentifiers(t *testing.T) {
	_, ts, fs := importServer(t)
	doc := v1alpha1.GovernancePolicy{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: "repeated-long-identifiers"},
		Spec: v1alpha1.GovernancePolicySpec{
			// No charset restriction: these bytes expand sixfold in JSON
			// and the team is repeated in every lease and authority budget.
			Subject: v1alpha1.Subject{Team: strings.Repeat("<&", 32<<10)},
		},
	}
	for i := 0; i < 256; i++ {
		doc.Spec.Rules = append(doc.Spec.Rules, v1alpha1.Rule{
			Name: fmt.Sprintf("cap-%03d", i), FailurePolicy: v1alpha1.FailClosed,
			Budget: &v1alpha1.BudgetRule{
				LimitMilliUSD: 100, HardCap: true,
				Lease: v1alpha1.LeaseSpec{GrantMilliUSD: 10, RenewInterval: "5s"},
			},
		})
	}
	if _, err := policy.FromV1Alpha1(&doc); err != nil {
		t.Fatalf("long-identifier fixture must be valid: %v", err)
	}
	bundle, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	resp, b := postImport(t, ts, "w", "?dry_run=true", string(bundle))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "worst-case sync") {
		t.Fatalf("repeated long identifiers: %d %s, want 400 naming the worst-case sync bound", resp.StatusCode, b)
	}
	if fs.replaceCalls != 0 {
		t.Fatal("undistributable set reached the store")
	}
}

func worstCaseSyncFixture(t *testing.T) []v1alpha1.GovernancePolicy {
	t.Helper()
	doc := v1alpha1.GovernancePolicy{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: strings.Repeat("policy<&", 32), Generation: 1},
		Spec: v1alpha1.GovernancePolicySpec{
			Subject: v1alpha1.Subject{Team: strings.Repeat("team<&", 128)},
			Rules: []v1alpha1.Rule{
				{
					Name: strings.Repeat("monthly-cap<&", 32), FailurePolicy: v1alpha1.FailClosed,
					Budget: &v1alpha1.BudgetRule{
						LimitMilliUSD: 100, HardCap: true,
						Lease: v1alpha1.LeaseSpec{GrantMilliUSD: 10, RenewInterval: "5s"},
					},
				},
				{
					Name: strings.Repeat("daily-cap<&", 32), FailurePolicy: v1alpha1.FailClosed,
					Budget: &v1alpha1.BudgetRule{
						LimitMilliUSD: 100, HardCap: true, Period: v1alpha1.PeriodCalendarDay,
						Lease: v1alpha1.LeaseSpec{GrantMilliUSD: 10, RenewInterval: "5s"},
					},
				},
				{
					Name: strings.Repeat("routing<&", 32), FailurePolicy: v1alpha1.FailOpen,
					Routing: &v1alpha1.RoutingRule{BudgetTiers: &v1alpha1.BudgetTiersRule{
						BudgetRef: strings.Repeat("monthly-cap<&", 32), EnforceTargets: true,
						Tiers: []v1alpha1.BudgetTier{
							{ThresholdPercent: 80, Substitute: map[string]string{
								strings.Repeat("premium-a<&", 32): strings.Repeat("economy-a<&", 32),
								strings.Repeat("premium-b<&", 32): strings.Repeat("economy-b<&", 32),
								strings.Repeat("premium-c<&", 32): strings.Repeat("economy-c<&", 32),
							}},
							{ThresholdPercent: 95, Substitute: map[string]string{"premium": "economy"}},
						},
					}},
				},
			},
		},
	}
	if _, err := policy.FromV1Alpha1(&doc); err != nil {
		t.Fatalf("sync calibration fixture must be valid: %v", err)
	}
	return []v1alpha1.GovernancePolicy{doc}
}

func syncArrayElementBytes[T any](t *testing.T, elements []T) int {
	t.Helper()
	total := 0
	for _, element := range elements {
		raw, err := json.Marshal(element)
		if err != nil {
			t.Fatal(err)
		}
		total += len(raw) + 1
	}
	return total
}

func TestWorstCaseSyncBoundsRealHeartbeat(t *testing.T) {
	docs := worstCaseSyncFixture(t)
	raw, err := yaml.Marshal(docs[0])
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer("t", dir)
	if err != nil {
		t.Fatal(err)
	}
	// Hold the clock within one window so the latch check cannot race midnight.
	now := time.Date(2026, 10, 15, 12, 0, 0, 123456789, time.UTC)
	s.now = func() time.Time { return now }
	mux := http.NewServeMux()
	s.Mount(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	active := doSync(t, ts.URL, "t", policy.SyncRequest{
		Dataplane: "dp", APIVersions: policy.SupportedAPIVersions,
		Reports: []policy.ConsumptionReport{{
			Policy: docs[0].Metadata.Name, Rule: docs[0].Spec.Rules[0].Name,
			Team: docs[0].Spec.Subject.Team, SpentMicroUSD: 80_000,
		}},
	})
	if len(active.ActiveTiers) != 1 || active.ActiveTiers[0].ThresholdPercent != 80 || len(active.ActiveTiers[0].Substitute) != 3 {
		t.Fatalf("consumption did not activate the largest tier: %+v", active.ActiveTiers)
	}
	// Drop spend below the activation threshold: the measured response must
	// still carry the latched tier, plus the full policy set and leases.
	requestBody, err := json.Marshal(policy.SyncRequest{
		Dataplane: "dp", APIVersions: policy.SupportedAPIVersions,
		Reports: []policy.ConsumptionReport{{
			Policy: docs[0].Metadata.Name, Rule: docs[0].Spec.Rules[0].Name,
			Team: docs[0].Spec.Subject.Team, SpentMicroUSD: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1alpha1/sync", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sync status %d: %s", resp.StatusCode, body)
	}
	var real policy.SyncResponse
	if err := json.Unmarshal(body, &real); err != nil {
		t.Fatal(err)
	}
	if len(real.Policies) != 1 || len(real.Leases) != 2 || len(real.ActiveTiers) != 1 ||
		real.ActiveTiers[0].ThresholdPercent != 80 || len(real.ActiveTiers[0].Substitute) != 3 {
		t.Fatalf("calibration must include policies, leases and the latched tier: %+v", real)
	}
	size, exceeded, err := worstCaseSyncSize(docs, now, importMaxSyncBytes)
	if err != nil {
		t.Fatal(err)
	}
	if exceeded {
		t.Fatalf("calibration fixture exceeded sync bound: %+v", size)
	}
	for _, component := range []struct {
		name            string
		measured, bound int
	}{
		{"Policies", syncArrayElementBytes(t, real.Policies), size.Policies},
		{"Leases", syncArrayElementBytes(t, real.Leases), size.Leases},
		{"ActiveTiers", syncArrayElementBytes(t, real.ActiveTiers), size.ActiveTiers},
	} {
		t.Logf("memory %s: measured=%d bytes, bound=%d bytes", component.name, component.measured, component.bound)
		if component.measured > component.bound {
			t.Errorf("%s %d bytes exceeds component bound %d bytes", component.name, component.measured, component.bound)
		}
	}
	fixed := size.Total - size.RequestDriven
	t.Logf("memory heartbeat: measured=%d bytes, fixed bound=%d bytes, total=%d bytes", len(body), fixed, size.Total)
	if len(body) > fixed {
		t.Fatalf("real heartbeat %d bytes exceeds fixed sync bound %d bytes", len(body), fixed)
	}
}

func TestWorstCaseActiveTierCountsThresholdWidth(t *testing.T) {
	docs := worstCaseSyncFixture(t)
	doc := &docs[0]
	for i := range doc.Spec.Rules {
		rule := &doc.Spec.Rules[i]
		if rule.Routing == nil || rule.Routing.BudgetTiers == nil {
			continue
		}
		bt := rule.Routing.BudgetTiers
		substitute := map[string]string{"premium": "economy"}
		bt.Tiers = []v1alpha1.BudgetTier{
			{ThresholdPercent: 1, Substitute: substitute},
			{ThresholdPercent: 100, Substitute: substitute},
		}
		active := policy.ActiveTier{
			Policy: doc.Metadata.Name, Rule: rule.Name, BudgetRef: bt.BudgetRef,
			Team: doc.Spec.Subject.Team, User: doc.Spec.Subject.User,
			ThresholdPercent: 100, Substitute: substitute, EnforceTargets: bt.EnforceTargets,
		}
		raw, err := json.Marshal(active)
		if err != nil {
			t.Fatal(err)
		}
		size, exceeded, err := worstCaseSyncSize(docs, time.Now(), importMaxSyncBytes)
		if err != nil {
			t.Fatal(err)
		}
		if exceeded {
			t.Fatalf("threshold-width fixture exceeded sync bound: %+v", size)
		}
		if size.ActiveTiers < len(raw)+1 {
			t.Fatalf("active tier bound %d bytes, want >= %d bytes for threshold 100", size.ActiveTiers, len(raw)+1)
		}
		return
	}
	t.Fatal("fixture must include a budgetTiers rule")
}

func TestWorstCaseSyncCoversAuthorityEnvelope(t *testing.T) {
	docs := worstCaseSyncFixture(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 123456789, time.UTC)
	budgets, err := policy.AuthorityBudgets(docs, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(budgets) != 2 {
		t.Fatalf("calibration must include two authority budgets, got %d", len(budgets))
	}
	size, exceeded, err := worstCaseSyncSize(docs, now, importMaxSyncBytes)
	if err != nil {
		t.Fatal(err)
	}
	if exceeded {
		t.Fatalf("calibration fixture exceeded sync bound: %+v", size)
	}
	generation := policy.GenerationOf(docs)
	fingerprint := "identity-config-v1:" + strings.Repeat("f", 64)
	env := policy.SyncResponse{
		IdentityFingerprint: fingerprint,
		Policies:            docs, Generation: generation, SyncIntervalSeconds: 10,
		Authority: &policy.AuthorityResponse{
			IdentityFingerprint: fingerprint,
			AuthorityID:         strings.Repeat("f", 64),
			Protocol:            policy.AuthorityProtocol, ServerTime: now, Budgets: budgets,
			Generation: generation, Policies: docs,
		},
	}
	for _, component := range []struct {
		name            string
		measured, bound int
	}{
		{"AuthorityPolicies", syncArrayElementBytes(t, env.Authority.Policies), size.AuthorityPolicies},
		{"Budgets", syncArrayElementBytes(t, env.Authority.Budgets), size.Budgets},
	} {
		t.Logf("authority %s: measured=%d bytes, bound=%d bytes", component.name, component.measured, component.bound)
		if component.measured > component.bound {
			t.Errorf("%s %d bytes exceeds component bound %d bytes", component.name, component.measured, component.bound)
		}
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	fixed := size.Total - size.RequestDriven
	t.Logf("authority envelope: measured=%d bytes, fixed bound=%d bytes, total=%d bytes", len(raw), fixed, size.Total)
	if len(raw) > fixed {
		t.Fatalf("authority envelope %d bytes exceeds fixed sync bound %d bytes", len(raw), fixed)
	}
	largestBudget := budgets[0]
	largestKey := budgets[0].Key
	for _, b := range budgets[1:] {
		if len(b.Key)+len(b.Revision)+len(b.WindowID) > len(largestBudget.Key)+len(largestBudget.Revision)+len(largestBudget.WindowID) {
			largestBudget = b
		}
		if len(b.Key) > len(largestKey) {
			largestKey = b.Key
		}
	}
	maxDigest := strings.Repeat("f", 64)
	grant := policy.AuthorityGrant{
		ID: maxDigest, RequestID: maxDigest,
		Key: largestBudget.Key, Revision: largestBudget.Revision, WindowID: largestBudget.WindowID,
		Amount: math.MaxInt64, ExpiresAt: worstCaseSyncTimestamp(),
	}
	ack := policy.AuthorityAck{
		Instance: maxDigest, ID: maxDigest, Sequence: math.MaxInt64,
	}
	denial := policy.AuthorityDenial{
		RequestID: maxDigest, Key: largestKey, Reason: strings.Repeat("x", 512),
	}
	for _, item := range []struct {
		name  string
		value any
	}{
		{"grant", grant},
		{"ack", ack},
		{"denial", denial},
	} {
		raw, err := json.Marshal(item.value)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw)+1 > size.AuthorityItemMax {
			t.Errorf("%s %d bytes exceeds authority item maximum %d bytes", item.name, len(raw)+1, size.AuthorityItemMax)
		}
	}
	if size.AuthorityItems != 1024*size.AuthorityItemMax {
		t.Errorf("authority items bound %d bytes, want %d", size.AuthorityItems, 1024*size.AuthorityItemMax)
	}
	if size.RequestEcho != 1<<20 {
		t.Errorf("request echo bound %d bytes, want %d", size.RequestEcho, 1<<20)
	}
	if size.RequestDriven != size.AuthorityItems+size.RequestEcho {
		t.Errorf("request-driven bound %d bytes, want %d", size.RequestDriven, size.AuthorityItems+size.RequestEcho)
	}
	skeleton := policy.SyncResponse{
		IdentityFingerprint:      fingerprint,
		IdentityPoliciesComplete: true,
		Generation:               maxDigest,
		Policies:                 []v1alpha1.GovernancePolicy{},
		Leases:                   []policy.LeaseGrant{},
		ActiveTiers:              []policy.ActiveTier{},
		SyncIntervalSeconds:      math.MaxInt32,
		Authority: &policy.AuthorityResponse{
			IdentityFingerprint: fingerprint,
			AuthorityID:         maxDigest,
			Protocol:            policy.AuthorityProtocol,
			ServerTime:          worstCaseSyncTimestamp(),
			Generation:          maxDigest,
			Policies:            []v1alpha1.GovernancePolicy{},
			Budgets:             []policy.AuthorityBudget{},
			Grants:              []policy.AuthorityGrant{grant},
			ReportAcks:          []policy.AuthorityAck{ack},
			MeterAcks:           []policy.AuthorityAck{ack},
			Denied:              []policy.AuthorityDenial{denial},
		},
	}
	raw, err = json.Marshal(skeleton)
	if err != nil {
		t.Fatal(err)
	}
	scalars := len(raw)
	for _, item := range []any{grant, ack, ack, denial} {
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		scalars -= len(raw)
	}
	if scalars > size.Scalars+4 {
		t.Errorf("authority skeleton %d bytes exceeds scalar bound plus four separators %d bytes", scalars, size.Scalars+4)
	}
	var grants []policy.AuthorityGrant
	var denials []policy.AuthorityDenial
	var acks []policy.AuthorityAck
	for i := 0; i < 1024; i++ {
		b := budgets[i%len(budgets)]
		grants = append(grants, policy.AuthorityGrant{
			ID: fmt.Sprintf("%064x", i), RequestID: fmt.Sprintf("%064x", i+1024),
			Key: b.Key, Revision: b.Revision, WindowID: b.WindowID,
			Amount: math.MaxInt64, ExpiresAt: now,
		})
		denials = append(denials, policy.AuthorityDenial{
			RequestID: fmt.Sprintf("%064x", i+1024), Key: b.Key, Reason: "closed_grant",
		})
		acks = append(acks, policy.AuthorityAck{
			Instance: fmt.Sprintf("%064x", i+2048), ID: fmt.Sprintf("%064x", i),
			Sequence: math.MaxInt64,
		})
	}
	for _, batch := range []struct {
		name  string
		items any
	}{
		{"grants", grants},
		{"denials", denials},
		{"acks", acks},
	} {
		t.Run(batch.name, func(t *testing.T) {
			raw, err := json.Marshal(batch.items)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("1024 %s: measured=%d bytes, request-driven bound=%d bytes", batch.name, len(raw), size.RequestDriven)
			if len(raw) > size.RequestDriven {
				t.Fatalf("%s batch %d bytes exceeds request-driven bound %d bytes", batch.name, len(raw), size.RequestDriven)
			}
		})
	}
}

func TestWorstCaseSyncEarlyExit(t *testing.T) {
	doc := v1alpha1.GovernancePolicy{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: "early-exit"},
		Spec: v1alpha1.GovernancePolicySpec{
			Subject: v1alpha1.Subject{Team: strings.Repeat("<", 1<<20)},
		},
	}
	for i := 0; i < 256; i++ {
		doc.Spec.Rules = append(doc.Spec.Rules, v1alpha1.Rule{
			Name: fmt.Sprintf("cap-%03d", i), FailurePolicy: v1alpha1.FailClosed,
			Budget: &v1alpha1.BudgetRule{
				LimitMilliUSD: 100, HardCap: true,
				Lease: v1alpha1.LeaseSpec{GrantMilliUSD: 10, RenewInterval: "5s"},
			},
		})
	}
	if _, err := policy.FromV1Alpha1(&doc); err != nil {
		t.Fatalf("early-exit fixture must be valid: %v", err)
	}
	now := time.Now()
	docs := []v1alpha1.GovernancePolicy{doc}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	size, exceeded, err := worstCaseSyncSize(docs, now, importMaxSyncBytes)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if !exceeded || size.Total <= importMaxSyncBytes {
		t.Fatalf("oversized repeated identifiers must exceed sync bound: %+v, exceeded=%v", size, exceeded)
	}
	if size.Leases != 0 || size.ActiveTiers != 0 || size.Budgets != 0 || size.RequestDriven != 0 {
		t.Fatalf("measurement continued past the repeated policy early exit: %+v", size)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("early exit: measured=%d bytes, TotalAlloc=%d bytes (limit=%d bytes)", size.Total, allocated, 256<<20)
	if allocated >= 256<<20 {
		t.Fatalf("early-exit TotalAlloc=%d bytes, want < %d bytes", allocated, 256<<20)
	}
}

func TestWorstCaseTimestampMarshals(t *testing.T) {
	raw, err := json.Marshal(worstCaseSyncTimestamp())
	if err != nil {
		t.Fatal(err)
	}
	const want = `"9999-12-31T23:59:59.999999999-23:59"`
	if string(raw) != want || len(raw) != 35+2 {
		t.Fatalf("maximum timestamp = %s (%d bytes), want %s (37 bytes)", raw, len(raw), want)
	}
}
