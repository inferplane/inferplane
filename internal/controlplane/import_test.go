package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "sync") {
		t.Fatalf("oversized set: %d %s, want 400 naming the sync bound", resp.StatusCode, b)
	}
	if fs.replaceCalls != 0 {
		t.Fatal("undistributable set reached the store")
	}
}

func TestImportSizeEstimateCountsRules(t *testing.T) {
	_, ts, fs := importServer(t)
	// Three rules, including two non-budget rules: every kind contributes
	// to the conservative estimate.
	bundle := cpPolicyYAML + "---\n" + importDocB
	docs, err := policy.ParseWireDocs([]byte(bundle))
	if err != nil {
		t.Fatal(err)
	}
	setJSON, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	prev := importMaxSyncBytes
	importMaxSyncBytes = 2*len(setJSON) + importEnvelopeFixedBytes + 2*importEnvelopeBytesPerRule
	t.Cleanup(func() { importMaxSyncBytes = prev })
	resp, b := postImport(t, ts, "w", "?dry_run=true", bundle)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "sync") {
		t.Fatalf("per-rule estimate: %d %s, want 400 naming the sync bound", resp.StatusCode, b)
	}
	if fs.replaceCalls != 0 {
		t.Fatal("undistributable set reached the store")
	}
}
