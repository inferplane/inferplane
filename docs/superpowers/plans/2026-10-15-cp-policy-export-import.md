# Control-plane policy export / import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Back up and atomically restore the GovernancePolicy set that `inferplaned` distributes, through an API (`POST /v1alpha1/config/import`, dry-run plus apply) and console buttons.

**Architecture:**
- `policystore.Store` gains `Replace`, which swaps the whole document set in one Postgres transaction. Policy listing becomes byte-ordered so generations agree everywhere.
- `controlplane.Server.ApplyImport` validates the full bundle BEFORE any write. It then diffs the bundle by document name, checks an `expected_generation` precondition under a new `writeMu`, commits through `Replace`, and installs the validated snapshot without a DB re-read.
- The CP console downloads the existing export as a file. It uploads a file, shows the dry-run diff, and applies it with the previewed generation.

**Tech Stack:** Go 1.26 (stdlib `net/http`, `sigs.k8s.io/yaml`, `pgx/v5`), vanilla JS console tested by the stdlib-only Node harness in `internal/controlplane/ui/testdata/`.

**Spec:** `docs/superpowers/specs/2026-10-15-cp-policy-export-import-design.md`

## Execution Order

Tasks 1 and 3 have disjoint file sets, so they run as one parallel wave. Task 3 tests only against a mocked HTTP contract. Task 2 depends on Task 1's `Replace` and test hooks, so it runs LAST, and its Step 10 full-repository verification therefore follows every source change.

## Global Constraints

- Every source change is `gofmt`-clean and `go vet`-clean. Errors are wrapped with `%w`.
- Commits are DCO signed off (`git commit -s`). The harness host is the only committer.
- Do not echo store/pgx error text to HTTP callers. A store failure maps to `503` with the fixed body `policy store unavailable`. Use the existing `writePolicyError`.
- `ErrPolicyValidation` messages ARE echoed (policies carry no secret values).
- Import size contract:
  - Raw YAML is capped by `importMaxBodyBytes` (64 MiB).
  - The parsed set's JSON must fit `importMaxSetJSONBytes` (8 MiB, the data-plane sync decode bound).
  - The per-document PUT bound `policyMaxBodyBytes` (1 MiB) is unchanged.
- Import auth is `authnWrite`. The heartbeat token is refused with `403`.
- Scope is GovernancePolicy documents only. Never touch authority ledger, usage telemetry or identity configuration.
- Console UI strings are English. The CP console has no i18n layer.
- `internal/policystore` stays a leaf: it never parses YAML.

## Review Focus

1. **A bundle that is an exact export of the current set.** Expect a dry run with every document `unchanged` and `result_generation == current_generation`. Pinned in Task 2 (`TestImportRoundTripIsNoop`).
2. **A YAML bundle with comments, key reordering or JSON syntax that is semantically identical to a stored doc.** Expect `unchanged`, not `changed`. The diff compares canonical re-marshals, not raw bytes. Pinned in Task 2 (`TestImportDiffIgnoresFormatting`).
3. **Two writers at once:** an import racing a single-document PUT. Expect the second writer to observe the first's generation, so a stale import gets 409 and never silently overwrites. Pinned in Task 2 (`TestImportSerializesWithApplyWrite`).
4. **An import while `Replace` fails** (DB down mid-restore). Expect 503, with both the store and the in-memory generation unchanged. Pinned in Task 2 (`TestImportStoreFailureLeavesStateUntouched`). The DB becoming unreadable just AFTER the commit must still install the previewed generation; that is pinned by `TestImportInstallsCommittedSetWithoutReread`.
5. **A console session whose policies changed between preview and apply.** Expect a 409 shown as "Policies changed since preview; re-run preview." with the dialog left open. Pinned in Task 3 (`policy_import.cjs` 409 case).

---

### Task 1: Atomic `Store.Replace` + byte-order policy listing

**Files:**
- Modify: `internal/policystore/policystore.go`
- Modify: `internal/policystore/postgres.go`
- Test: `internal/policystore/postgres_test.go`
- Modify: `internal/authority/pgstore/pgstore.go`
- Modify: `internal/controlplane/policies_test.go`
- Modify: `internal/CLAUDE.md`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `policystore.Store.Replace(ctx context.Context, docs []Doc) error`. The stored set becomes exactly `docs`, in one transaction bounded by `pgWriteTimeout` (30s), with the seed marker set.
  - `PostgresStore.List` and authority `readPolicies` both order by `name COLLATE "C"`, which is byte order and identical to Go's `<` on strings. Every `GenerationOf` computed from the DB therefore matches one computed from a Go-sorted slice.
  - The `fakeStore` test double in `internal/controlplane/policies_test.go` gains `Replace` and these injection hooks, which Task 2 uses:
    - `replaceErr error`
    - `replaceCalls int`
    - `replaceEntered chan struct{}`, signaled on entry
    - `replaceGate chan struct{}`, which `Replace` waits on when non-nil
    - `putStarted chan struct{}`, non-blocking signal on entry

    All channel hooks fire BEFORE `f.mu` is taken, so the fake's own mutex can never stand in for the server's `writeMu`.

- [ ] **Step 1: Write the failing test**

Append to `internal/policystore/postgres_test.go`:

```go
func TestReplaceSwapsWholeSetAndMarksSeeded(t *testing.T) {
	p := newTestStore(t)
	ctx := context.Background()
	if err := p.Put(ctx, "old-a", []byte("doc: old-a")); err != nil {
		t.Fatal(err)
	}
	if err := p.Put(ctx, "keep", []byte("doc: keep-v1")); err != nil {
		t.Fatal(err)
	}
	if err := p.Replace(ctx, []Doc{
		{Name: "keep", YAML: []byte("doc: keep-v2")},
		{Name: "new-b", YAML: []byte("doc: new-b")},
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	docs, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Name != "keep" || docs[1].Name != "new-b" {
		t.Fatalf("after Replace got %+v, want exactly [keep new-b]", docs)
	}
	if string(docs[0].YAML) != "doc: keep-v2" {
		t.Fatalf("keep not overwritten: %q", docs[0].YAML)
	}
	seeded, err := p.Seeded(ctx)
	if err != nil || !seeded {
		t.Fatalf("Seeded after Replace = %v, %v; want true", seeded, err)
	}
	// Replace with nothing empties the store but keeps the marker, so a
	// restored-empty store is never re-seeded from --policies files.
	if err := p.Replace(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if docs, _ := p.List(ctx); len(docs) != 0 {
		t.Fatalf("Replace(nil) left %d docs", len(docs))
	}
	if seeded, _ := p.Seeded(ctx); !seeded {
		t.Fatal("Replace(nil) cleared the seed marker")
	}
}

func TestReplaceRollsBackOnFailure(t *testing.T) {
	p := newTestStore(t)
	ctx := context.Background()
	if err := p.Put(ctx, "survivor", []byte("doc: survivor")); err != nil {
		t.Fatal(err)
	}
	// A duplicate name violates the primary key on the second INSERT, after
	// the DELETE already ran inside the transaction: everything must roll back.
	err := p.Replace(ctx, []Doc{
		{Name: "dup", YAML: []byte("doc: 1")},
		{Name: "dup", YAML: []byte("doc: 2")},
	})
	if err == nil {
		t.Fatal("Replace with duplicate names succeeded; want an error")
	}
	docs, lerr := p.List(ctx)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(docs) != 1 || docs[0].Name != "survivor" {
		t.Fatalf("failed Replace was not rolled back: %+v", docs)
	}
}

// List must be byte-ordered regardless of the database collation: the
// controlplane's GenerationOf is order-sensitive and is compared against a
// Go-sorted import preview and against authority/pgstore's own read. Under
// en_US collation "team-b" sorts after "teama"; in byte order it sorts before.
func TestListIsByteOrderedNotCollationOrdered(t *testing.T) {
	p := newTestStore(t)
	ctx := context.Background()
	for _, n := range []string{"teama", "team-b", "Team-c"} {
		if err := p.Put(ctx, n, []byte("doc: "+n)); err != nil {
			t.Fatal(err)
		}
	}
	docs, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{docs[0].Name, docs[1].Name, docs[2].Name}
	want := []string{"Team-c", "team-b", "teama"} // bytewise: 'T' < 't', '-' < 'a'
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List order %v, want byte order %v", got, want)
		}
	}
	if !strings.Contains(policyListSQL, `COLLATE "C"`) {
		t.Fatal(`List query must pin COLLATE "C" so the order does not depend on the server's locale`)
	}
}
```

The authority reader's matching `COLLATE "C"` change (Step 4) is a one-line SQL edit. Its existing Postgres-backed tests in `internal/authority/pgstore` must keep passing in Step 6.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/policystore/ -run 'TestReplace|TestListIsByteOrdered' -v`
Expected: FAIL to compile with `p.Replace undefined (type *PostgresStore has no field or method Replace)`.

- [ ] **Step 3: Add `Replace` to the interface**

In `internal/policystore/policystore.go`, add inside `type Store interface`, after `Seed`:

```go
	// Replace atomically makes docs the COMPLETE stored set: every existing
	// row is deleted and docs inserted in ONE transaction, and the seed
	// marker is set so a restored store is never re-seeded from files.
	// Callers validate docs first; this package never parses them. A failure
	// leaves the previous set intact.
	Replace(ctx context.Context, docs []Doc) error
```

- [ ] **Step 4: Implement `PostgresStore.Replace` and byte-order listing**

In `internal/policystore/postgres.go`, update the lock-key comment and add the new constants next to `policySchemaLockKey`:

```go
// policySchemaLockKey is a Postgres advisory-lock key distinct from
// analytics' (847001), bodystore's (847002), telemetry's (847003) and
// authority's (847005, plus 847007 in authority/pgstore/shared.go).
const policySchemaLockKey int64 = 847004

// policyReplaceLockKey serializes concurrent Replace transactions so two
// imports never interleave rows. 847006 is unused elsewhere (grep 8470).
const policyReplaceLockKey int64 = 847006

// pgWriteTimeout bounds a whole-set Replace. The controlplane holds its
// writer mutex across the call, so an unbounded stall would freeze every
// policy PUT/DELETE/import.
const pgWriteTimeout = 30 * time.Second

// policyListSQL orders by BYTE order (COLLATE "C"), never the server locale:
// policy.GenerationOf is order-sensitive, and the controlplane's generation
// must equal both a Go-sorted import preview and authority/pgstore's read.
const policyListSQL = `SELECT name, doc_yaml, updated_at FROM policies ORDER BY name COLLATE "C"`
```

In `List`, replace the literal query string with `policyListSQL`. The current literal is `` `SELECT name, doc_yaml, updated_at FROM policies ORDER BY name` `` at `postgres.go:98`.

In `internal/authority/pgstore/pgstore.go` `readPolicies`, change the query to:

```go
	rows, err := tx.Query(ctx, `SELECT name, doc_yaml FROM policies ORDER BY name COLLATE "C"`)
```

Its `GenerationOf(docs)` (`pgstore.go:209`, `identity.go:51`, `shared_constraints.go:84`) is compared with the generation the controlplane distributes, so both readers MUST use the same order.

Add the method before `var _ Store = (*PostgresStore)(nil)`:

```go
// Replace swaps the whole stored set in one transaction (see Store.Replace),
// bounded by pgWriteTimeout from entry (schema check included).
func (p *PostgresStore) Replace(ctx context.Context, docs []Doc) error {
	ctx, cancel := context.WithTimeout(ctx, pgWriteTimeout)
	defer cancel()
	if err := p.ensureSchema(ctx); err != nil {
		return err
	}
	tx, err := p.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("policystore: replace begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, policyReplaceLockKey); err != nil {
		return fmt.Errorf("policystore: replace lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM policies`); err != nil {
		return fmt.Errorf("policystore: replace clear: %w", err)
	}
	for _, d := range docs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO policies (name, doc_yaml, updated_at) VALUES ($1, $2, now())`,
			d.Name, string(d.YAML)); err != nil {
			return fmt.Errorf("policystore: replace policy %q: %w", d.Name, err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO policy_meta (key, value) VALUES ($1, '1') ON CONFLICT (key) DO UPDATE SET value = '1'`,
		seededKey); err != nil {
		return fmt.Errorf("policystore: replace mark seeded: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("policystore: replace commit: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Give the controlplane test double `Replace` and its hooks**

In `internal/controlplane/policies_test.go`, add these fields to `type fakeStore struct`, after `listErr error`:

```go
	replaceErr   error
	replaceCalls int
	// Concurrency hooks (Task 2's writeMu test). Each fires BEFORE f.mu is
	// taken, so the fake's own mutex can never stand in for the server's
	// writer serialization.
	replaceEntered chan struct{} // signaled (non-blocking; give it capacity 1) when Replace is entered
	replaceGate    chan struct{} // Replace waits for it to close when non-nil
	putStarted     chan struct{} // non-blocking signal when Put is entered
	// Post-commit fault injection (Task 2):
	listErrAfterReplace  error // installed as listErr once a Replace succeeds
	replaceCommitThenErr error // Replace commits the new set, then returns this (lost-ack)
```

At the very top of `fakeStore.Put`, before `f.mu.Lock()`, add:

```go
	if f.putStarted != nil {
		select {
		case f.putStarted <- struct{}{}:
		default:
		}
	}
```

Add the method after `fakeStore.Seed`:

```go
func (f *fakeStore) Replace(_ context.Context, docs []policystore.Doc) error {
	if f.replaceEntered != nil {
		select {
		case f.replaceEntered <- struct{}{}:
		default:
		}
	}
	if f.replaceGate != nil {
		<-f.replaceGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaceCalls++
	if f.replaceErr != nil {
		return f.replaceErr
	}
	next := make(map[string]policystore.Doc, len(docs))
	for _, d := range docs {
		next[d.Name] = policystore.Doc{Name: d.Name, YAML: append([]byte(nil), d.YAML...), UpdatedAt: time.Now()}
	}
	f.docs = next
	f.seeded = true
	if f.listErrAfterReplace != nil {
		f.listErr = f.listErrAfterReplace
	}
	return f.replaceCommitThenErr
}
```

Add one sentence to the `policystore/` bullet in `internal/CLAUDE.md` (same-commit sync rule):

```markdown
 `Replace` swaps the WHOLE set in one transaction (advisory-locked, 30s-bounded) and sets the seed marker — the import/restore primitive; a failure leaves the prior set. `List` and authority `readPolicies` order by `name COLLATE "C"` (byte order) because `GenerationOf` is order-sensitive and both readers' generations are compared.
```

- [ ] **Step 6: Run the tests to verify they pass**

Postgres tests skip without a DSN. Run them against a throwaway local container:

```bash
mkdir -p /tmp/claude-1001
# Debian image on purpose: glibc en_US.utf8 collation orders "teama" < "team-b",
# so TestListIsByteOrderedNotCollationOrdered genuinely fails without COLLATE "C".
# (musl/alpine collation is already byte order and would make it vacuous.)
docker run -d --rm --name ip-pg-test -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:17
until docker exec ip-pg-test pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
docker exec ip-pg-test psql -U postgres -tAc "SELECT datcollate FROM pg_database WHERE datname='postgres'"   # expect en_US.utf8
INFERPLANE_TEST_PG_DSN='postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable' go test ./internal/policystore/ ./internal/authority/... -race -v > /tmp/claude-1001/pgtest.log 2>&1; echo rc=$?
grep -E "^(=== RUN|--- (PASS|FAIL|SKIP)|ok|FAIL)" /tmp/claude-1001/pgtest.log
docker stop ip-pg-test
go test ./internal/controlplane/ -race > /tmp/claude-1001/cptest.log 2>&1; echo rc=$?
```

Expected: `rc=0` for both. `TestReplaceSwapsWholeSetAndMarksSeeded`, `TestReplaceRollsBackOnFailure` and `TestListIsByteOrderedNotCollationOrdered` show `--- PASS` (not SKIP). The authority tests pass unchanged. Check which authority tests use the DSN with `grep -l INFERPLANE_TEST_PG_DSN internal/authority -r`. The controlplane package still compiles and passes.

- [ ] **Step 7: Commit**

```bash
git add internal/policystore/policystore.go internal/policystore/postgres.go internal/policystore/postgres_test.go internal/authority/pgstore/pgstore.go internal/controlplane/policies_test.go internal/CLAUDE.md
git commit -s -m "feat(policystore): atomic Replace and byte-order listing for policy restore"
```

---

### Task 2: `ApplyImport` + `POST /v1alpha1/config/import`

**Files:**
- Create: `internal/controlplane/import.go`
- Test: `internal/controlplane/import_test.go`
- Modify: `internal/controlplane/policies.go` (`ApplyWrite`, `ApplyDelete`, `MutationEntry` comment)
- Modify: `internal/controlplane/controlplane.go` (`Server` struct field, `Mount`)
- Modify: `internal/controlplane/export.go`
- Modify: `scripts/prepare-policy-controlplane.py`
- Test: `tests/deployment/test_policy_store.py`
- Modify: `docs/reference/api.md`
- Modify: `docs/reference/api.ko.md`
- Modify: `docs/runbooks/inferplaned-policy-store.md`
- Modify: `docs/runbooks/inferplaned-policy-store.ko.md`
- Modify: `internal/CLAUDE.md`

**Interfaces:**
- Consumes: `policystore.Store.Replace` (Task 1), and from `policies_test.go` the `fakeStore` fields `replaceErr`, `replaceCalls`, `replaceEntered`, `replaceGate`, `putStarted` and `listErr` (Task 1 / existing). Also `newTestServerWrite(t, token, writeToken) (*Server, *httptest.Server)` and `cpPolicyYAML` (existing, `controlplane_test.go`).
- Produces (Task 3 consumes the HTTP contract):
  - `POST /v1alpha1/config/import?dry_run=true|expected_generation=<g>[&allow_empty=true]`
  - The 200 JSON body is `{"dry_run":bool,"current_generation":string,"result_generation":string,"changes":[{"name":string,"action":"added"|"changed"|"removed"|"unchanged"}],"summary":{"added":int,"changed":int,"removed":int,"unchanged":int}}`.
  - A 409 JSON body is `{"error":"policies changed since preview","current_generation":string}`, with `Cache-Control: no-store`.
  - Size contract: `importMaxBodyBytes = 64 << 20` is the raw YAML transport cap. `importMaxSetJSONBytes = 8 << 20` caps the parsed set's JSON encoding at the data plane's sync decode bound (`internal/proxy/sync.go:377`); a larger set is a 400. Task 3's console pre-check uses `67108864`.
  - Both preview and apply hold `writeMu` and reconcile with the store first. A store read failure is a 503.
  - `GET /v1alpha1/config/export` adds `Content-Disposition: attachment; filename="inferplane-policies-<first 12 of generation>.yaml"`.

- [ ] **Step 1: Write the failing tests**

Create `internal/controlplane/import_test.go`:

```go
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

// The fleet bound is semantic: a set whose JSON a data plane cannot decode
// is refused even when the YAML bundle fits the transport cap.
func TestImportRefusesUndistributableSet(t *testing.T) {
	_, ts, fs := importServer(t)
	prev := importMaxSetJSONBytes
	importMaxSetJSONBytes = 256
	t.Cleanup(func() { importMaxSetJSONBytes = prev })
	resp, b := postImport(t, ts, "w", "?dry_run=true", cpPolicyYAML+"---\n"+importDocB)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "JSON bound") {
		t.Fatalf("oversized set: %d %s, want 400 naming the JSON bound", resp.StatusCode, b)
	}
	if fs.replaceCalls != 0 {
		t.Fatal("undistributable set reached the store")
	}
}
```

The `"bad tier ref"` fixture mirrors `cpTierPolicyYAML` (`controlplane_test.go:38`) with a dangling `budgetRef`. `policy.FromV1Alpha1` rejects it at `internal/policy/policy.go:448`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/controlplane/ -run 'TestImport' -v`
Expected: FAIL to compile with `undefined: ImportResult`, `undefined: ImportSummary`, `undefined: ImportChange`.

- [ ] **Step 3: Add `writeMu` and serialize the existing writers**

In `internal/controlplane/controlplane.go`, add a field to `type Server struct`, directly after the existing `mu` field:

```go
	// writeMu serializes every policy-store writer (ApplyWrite, ApplyDelete,
	// ApplyImport) across its whole commit-then-reload sequence, so an
	// import's expected_generation check and its commit see no interleaved
	// write. Separate from mu so heartbeats never wait on a DB round trip.
	// Lock order: writeMu before mu, never the reverse.
	writeMu sync.Mutex
```

At the start of `ApplyWrite` in `internal/controlplane/policies.go`, before its first `s.mu.Lock()`, add:

```go
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
```

Add the same two lines at the start of `ApplyDelete`.

- [ ] **Step 4: Implement `import.go`**

Create `internal/controlplane/import.go`:

```go
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"time"

	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/policystore"
)

// ErrImportConflict is returned when an import's expected_generation no
// longer matches the distributed set: someone changed policies between the
// operator's dry-run preview and the apply. Surfaces as 409.
var ErrImportConflict = errors.New("policies changed since preview")

// ErrImportNotApplied means the store COMMITTED the bundle but the in-memory
// install refused it — unreachable for a validated bundle, but if it ever
// happens the caller must not read it as "nothing changed". Surfaces as 500.
var ErrImportNotApplied = errors.New("policies were committed to the store but could not be applied in memory")

// Size contract. The FLEET bound is semantic: a data plane decodes a sync
// response of at most 8 MiB of JSON (internal/proxy/sync.go: 8<<20), so an
// import whose parsed set exceeds importMaxSetJSONBytes as JSON is refused —
// it could never be distributed. The TRANSPORT bound is only a memory cap:
// block YAML adds per-line indentation that JSON lacks (policy nesting is
// schema-fixed, so the overhead is bounded; a multiline-string fixture
// measured ~4.9x, and newline-per-byte strings at maximum depth stay under
// ~7x), so 64 MiB of YAML admits the export of every set within the fleet
// bound. policyMaxBodyBytes (1 MiB) stays the per-document PUT bound.
const importMaxBodyBytes = 64 << 20

// importMaxSetJSONBytes is a var only so tests can lower it.
var importMaxSetJSONBytes = 8 << 20

// errStoreList marks a store READ failure inside ReloadFromStore, as opposed
// to the store holding rows the loader rejects. Import reconciliation treats
// only the former as "store unavailable": a restore must still be able to
// overwrite rejected rows.
var errStoreList = errors.New("list stored policies")

// ImportOptions are the POST /v1alpha1/config/import query parameters.
type ImportOptions struct {
	DryRun             bool
	ExpectedGeneration string
	AllowEmpty         bool
}

// ImportChange is one document's fate under a replace import.
type ImportChange struct {
	Name   string `json:"name"`
	Action string `json:"action"` // added | changed | removed | unchanged
}

// ImportSummary counts ImportChange actions.
type ImportSummary struct {
	Added     int `json:"added"`
	Changed   int `json:"changed"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`
}

// ImportResult is the dry-run preview or the applied outcome.
type ImportResult struct {
	DryRun            bool           `json:"dry_run"`
	CurrentGeneration string         `json:"current_generation"`
	ResultGeneration  string         `json:"result_generation"`
	Changes           []ImportChange `json:"changes"`
	Summary           ImportSummary  `json:"summary"`
}

// ApplyImport makes body (multi-document GovernancePolicy YAML, the exact
// GET /v1alpha1/config/export format) the COMPLETE policy set — backup
// restore, not merge. Unlike ApplyWrite, the whole bundle is validated
// BEFORE the store is touched: ParseWireDocs runs every document through
// policy.FromV1Alpha1, and names/duplicates/identity subjects are checked
// here, which is every way applyWire can reject a set.
//
// Both preview and apply hold writeMu and first reconcile memory with the
// store, so the previewed current_generation and the apply precondition are
// judged against what the store actually holds — never against memory left
// stale by a committed-but-unreloaded PUT/DELETE or an unacknowledged commit.
func (s *Server) ApplyImport(ctx context.Context, body []byte, opts ImportOptions) (*ImportResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	store := s.policyStore
	s.mu.Unlock()
	if store == nil {
		return nil, ErrNoPolicyStore
	}
	if err := s.ReloadFromStore(ctx); err != nil && errors.Is(err, errStoreList) {
		return nil, fmt.Errorf("controlplane: reconcile before import: %w", err)
	}
	// (A rejected stored set — out-of-band DB edit — falls through: memory
	// still holds the last set actually distributed, and the restore is
	// precisely the tool to overwrite those rows.)
	s.mu.Lock()
	identityConfig := s.identityConfig
	currentGen := s.generation
	current := append([]v1alpha1.GovernancePolicy(nil), s.wire...)
	s.mu.Unlock()

	docs, err := policy.ParseWireDocs(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicyValidation, err)
	}
	seen := make(map[string]bool, len(docs))
	for i := range docs {
		name := docs[i].Metadata.Name
		if name == "" {
			return nil, fmt.Errorf("%w: document %d: metadata.name is required", ErrPolicyValidation, i+1)
		}
		if seen[name] {
			return nil, fmt.Errorf("%w: duplicate policy name %q", ErrPolicyValidation, name)
		}
		seen[name] = true
	}
	if err := policy.ValidateIdentitySubjects(docs, identityConfig); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicyValidation, err)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Metadata.Name < docs[j].Metadata.Name })

	// Canonical bytes: one stored row per document, rendered the same way
	// export.go and AttachPolicyStore render, so the diff is semantic (a
	// comment or key-order change is "unchanged") and export→import is a
	// no-op.
	rows := make([]policystore.Doc, 0, len(docs))
	incoming := make(map[string][]byte, len(docs))
	for i := range docs {
		raw, err := yaml.Marshal(&docs[i])
		if err != nil {
			return nil, fmt.Errorf("%w: render policy %q: %v", ErrPolicyValidation, docs[i].Metadata.Name, err)
		}
		rows = append(rows, policystore.Doc{Name: docs[i].Metadata.Name, YAML: raw})
		incoming[docs[i].Metadata.Name] = raw
	}
	existing := make(map[string][]byte, len(current))
	for i := range current {
		raw, err := yaml.Marshal(&current[i])
		if err != nil {
			return nil, fmt.Errorf("controlplane: render current policy %q: %w", current[i].Metadata.Name, err)
		}
		existing[current[i].Metadata.Name] = raw
	}

	res := &ImportResult{DryRun: opts.DryRun, CurrentGeneration: currentGen}
	for _, row := range rows {
		prev, ok := existing[row.Name]
		switch {
		case !ok:
			res.Changes = append(res.Changes, ImportChange{Name: row.Name, Action: "added"})
			res.Summary.Added++
		case string(prev) == string(row.YAML):
			res.Changes = append(res.Changes, ImportChange{Name: row.Name, Action: "unchanged"})
			res.Summary.Unchanged++
		default:
			res.Changes = append(res.Changes, ImportChange{Name: row.Name, Action: "changed"})
			res.Summary.Changed++
		}
	}
	for name := range existing {
		if _, ok := incoming[name]; !ok {
			res.Changes = append(res.Changes, ImportChange{Name: name, Action: "removed"})
			res.Summary.Removed++
		}
	}
	sort.Slice(res.Changes, func(i, j int) bool { return res.Changes[i].Name < res.Changes[j].Name })
	if res.Changes == nil {
		res.Changes = []ImportChange{}
	}

	// Predict the installed generation from the SAME bytes ReloadFromStore
	// will parse, so preview and apply can never disagree.
	var reparsed []v1alpha1.GovernancePolicy
	for _, row := range rows {
		d, err := policy.ParseWireDocs(row.YAML)
		if err != nil {
			return nil, fmt.Errorf("%w: canonical policy %q: %v", ErrPolicyValidation, row.Name, err)
		}
		reparsed = append(reparsed, d...)
	}
	res.ResultGeneration = policy.GenerationOf(reparsed)
	if setJSON, err := json.Marshal(reparsed); err != nil || len(setJSON) > importMaxSetJSONBytes {
		return nil, fmt.Errorf("%w: the policy set exceeds the %d-byte JSON bound a data plane can receive", ErrPolicyValidation, importMaxSetJSONBytes)
	}

	if opts.DryRun {
		return res, nil
	}
	if len(docs) == 0 && !opts.AllowEmpty {
		return nil, fmt.Errorf("%w: bundle contains no GovernancePolicy documents; pass allow_empty=true to remove every policy", ErrPolicyValidation)
	}
	if opts.ExpectedGeneration == "" {
		return nil, fmt.Errorf("%w: expected_generation is required to apply an import; run with dry_run=true first", ErrPolicyValidation)
	}
	if opts.ExpectedGeneration != currentGen {
		return nil, ErrImportConflict
	}
	audit := func() {
		for _, c := range res.Changes {
			switch c.Action {
			case "added", "changed":
				s.recordMutation(ctx, "put", c.Name, incoming[c.Name])
			case "removed":
				s.recordMutation(ctx, "delete", c.Name, nil)
			}
		}
		s.recordMutation(ctx, "import", "", append([]byte{}, body...)) // never nil: an empty bundle still hashes
	}
	if err := store.Replace(ctx, rows); err != nil {
		// The outcome may be indeterminate (a commit whose acknowledgement
		// was lost). Read back on a detached, bounded ctx: if the store now
		// holds exactly the predicted set, the restore DID commit.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if rerr := s.ReloadFromStore(rctx); rerr == nil {
			s.mu.Lock()
			committed := s.generation == res.ResultGeneration
			s.mu.Unlock()
			if committed {
				audit()
				return res, nil
			}
		}
		return nil, fmt.Errorf("controlplane: import policies: %w", err)
	}
	// Committed: audit first (a committed restore is never unaudited), then
	// install the validated, reparsed snapshot directly — the exact set
	// ReloadFromStore would produce (store.List is byte-ordered, rows are
	// byte-sorted) — so a post-commit DB read failure or a canceled request
	// cannot leave memory on the replaced set.
	audit()
	committedAt := time.Now()
	if err := s.applyWire(reparsed, nil); err != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if rerr := s.ReloadFromStore(rctx); rerr != nil {
			log.Printf("inferplaned: policy import committed but reload failed: %v", rerr)
		}
		return nil, fmt.Errorf("%w: %v", ErrImportNotApplied, err)
	}
	updated := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		updated[row.Name] = committedAt
	}
	s.mu.Lock()
	s.updated = updated
	res.ResultGeneration = s.generation
	s.mu.Unlock()
	return res, nil
}

// mountImport registers POST /v1alpha1/config/import behind the policy
// WRITE authority (authnWrite): the heartbeat token is never accepted.
func (s *Server) mountImport(mux *http.ServeMux) {
	s.mu.Lock()
	writeToken := s.writeToken
	s.mu.Unlock()
	mux.HandleFunc("POST /v1alpha1/config/import", authnWrite(s.token, writeToken, s.authOpts, s.handleConfigImport))
}

func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, importMaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "policy bundle too large")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "could not read request body")
		return
	}
	q := r.URL.Query()
	res, err := s.ApplyImport(r.Context(), body, ImportOptions{
		DryRun:             q.Get("dry_run") == "true",
		ExpectedGeneration: q.Get("expected_generation"),
		AllowEmpty:         q.Get("allow_empty") == "true",
	})
	if errors.Is(err, ErrImportConflict) {
		s.mu.Lock()
		gen := s.generation
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": ErrImportConflict.Error(), "current_generation": gen})
		return
	}
	if errors.Is(err, ErrImportNotApplied) {
		writeJSONError(w, http.StatusInternalServerError, "policies were committed but not applied; re-run a dry run to confirm the distributed set")
		return
	}
	if err != nil {
		writePolicyError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(res)
}
```

`applyWire` and `ReloadFromStore` take `s.mu` themselves. `ApplyImport` holds only `writeMu` when it calls them, so the lock order stays `writeMu` → `mu`.

In `internal/controlplane/policies.go` `ReloadFromStore`, wrap the `store.List` error with the new sentinel. Go 1.20+ supports multiple `%w`:

```go
	rows, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("controlplane: %w: %w", errStoreList, err)
	}
```

In `internal/controlplane/policies.go`, update the `MutationEntry` field comments:

```go
	Op          string // "put" | "delete" | "import" (one per restore, after its per-document entries)
	Name        string // the policy document name; "" for "import"
	ContentHash string // sha256: of the submitted body for ApplyWrite "put"; of the canonical re-marshal for an import's "put"; of the raw bundle for "import"; "" on delete
```

- [ ] **Step 5: Mount the route and update export**

In `internal/controlplane/controlplane.go` `Mount`, add after `s.mountPolicies(mux)`:

```go
	s.mountImport(mux)   // POST /v1alpha1/config/import (import.go)
```

In `internal/controlplane/export.go`:
- Replace the last paragraph of the `mountExport` doc comment ("The import endpoint is deliberately reserved (spec D5): … --policies.") with:

```go
// Its inverse is POST /v1alpha1/config/import (import.go): a replace-mode,
// dry-run-first restore of exactly this format into the policy store.
```

- In `handleConfigExport`, read the generation along with the wire, then set the header before writing the body:

```go
	s.mu.Lock()
	wire := s.wire
	gen := s.generation
	s.mu.Unlock()
```

```go
	short := gen
	if len(short) > 12 {
		short = short[:12]
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="inferplane-policies-`+short+`.yaml"`)
	_, _ = w.Write(buf.Bytes())
```

(Remove the old single `Content-Type` line so it is set once.)

- [ ] **Step 6: Update the API reference rows**

In `docs/reference/api.md`, replace the "CP config export" row (currently ending "…import reserved until a policy store exists |") with:

```markdown
| CP config export / import | `internal/controlplane/export.go`, `import.go` | **inferplaned**: `GET /v1alpha1/config/export` — the distributed policy set as multi-doc GovernancePolicy YAML (round-trips through the policy loader; secret-free; `Content-Disposition` attachment). `POST /v1alpha1/config/import` — replace-mode restore of that format into the policy store: `dry_run=true` returns the per-document diff (`added`/`changed`/`removed`/`unchanged`) and `result_generation`; apply requires `expected_generation` (409 on mismatch) and `allow_empty=true` for an empty bundle; whole bundle validated before one `policystore.Replace` transaction; `authnWrite` (heartbeat token 403); 405 without a store; 64 MiB YAML transport cap, and the parsed set must fit the 8 MiB JSON sync bound a data plane can decode (larger set ⇒ 400). Policies only — never ledger, usage or identity config |
```

In `docs/reference/api.ko.md`, replace the "CP 내보내기" row with:

```markdown
| CP 내보내기/가져오기 | `controlplane/export.go`, `import.go` | GET /v1alpha1/config/export(비밀 없는 다중 GovernancePolicy YAML, 첨부 파일로 다운로드), POST /v1alpha1/config/import(Replace 방식 복원). dry_run=true로 문서별 diff와 result_generation을 미리 확인하고, 적용 시 expected_generation이 필수(불일치 시 409). 빈 번들은 allow_empty=true가 필요합니다. 번들 전체를 검증한 뒤 트랜잭션 한 번으로 반영하고, authnWrite 권한이 필요합니다(heartbeat 토큰은 403). store가 없으면 405입니다. YAML 전송 한도는 64 MiB이고, 파싱된 세트는 데이터 플레인이 디코딩할 수 있는 8 MiB JSON sync 한도 안에 들어야 합니다(초과 시 400). 정책만 다루며 원장·사용량·identity 설정은 대상이 아닙니다 |
```

- [ ] **Step 7: Add the backup/restore runbook section**

Append to `docs/runbooks/inferplaned-policy-store.md`:

````markdown
## Back up and restore policies

The policy store holds GovernancePolicy documents only. A policy export does
NOT capture budget authority balances, usage telemetry or identity
configuration, and an import never restores them. Restoring a balance would
rewrite central authority. For the database itself, keep relying on the
RDS/pg_dump backups described above.

Back up with the read token (for example, from cron):

```bash
curl -fsS -H "Authorization: Bearer $INFERPLANED_TOKEN" \
  "$CP/v1alpha1/config/export" -o "policies-$(date -u +%Y%m%dT%H%M%SZ).yaml"
```

To restore, preview first and then apply with the previewed generation. Use the
policy write token:

```bash
curl -fsS -X POST -H "Authorization: Bearer $INFERPLANED_POLICY_WRITE_TOKEN" \
  --data-binary @policies.yaml "$CP/v1alpha1/config/import?dry_run=true" | tee preview.json
GEN=$(jq -r .current_generation preview.json)
curl -fsS -X POST -H "Authorization: Bearer $INFERPLANED_POLICY_WRITE_TOKEN" \
  --data-binary @policies.yaml "$CP/v1alpha1/config/import?expected_generation=$GEN"
```

Import REPLACES the whole set. Every document missing from the file is
deleted. A `409` means policies changed after the preview; re-run the preview.
An empty file needs `allow_empty=true`. Files are capped at 64 MiB, and a set
larger than the 8 MiB JSON a data plane can receive is refused with `400`. A `500`
saying "committed but not applied" means the store holds the new set; re-run a
dry run to confirm what is distributed. The console offers the same flow under
Policies → Export YAML / Import (restore).

If the distribution was prepared before this change, its `/v1alpha1/config/*`
requests still reach the legacy CP origin. `prepare-policy-controlplane.py`
refuses an existing policy origin, so add the `/v1alpha1/config/*` behavior
(policy-cp origin, `forward-auth` origin request policy) by reviewed manual
edit. Then invalidate the six policy/SSO/config paths.
````

Append the matching Korean section to `docs/runbooks/inferplaned-policy-store.ko.md`. Use the same code blocks verbatim, with the prose translated:

````markdown
## 정책 백업과 복원

policy store에는 GovernancePolicy 문서만 저장됩니다. 정책 내보내기에는 예산 authority 잔액, 사용량 텔레메트리, identity 설정이 포함되지 않으며, 가져오기도 이것들을 복원하지 않습니다. 잔액을 복원하면 중앙 authority를 다시 쓰는 셈이 되기 때문입니다. 데이터베이스 자체는 위에서 설명한 RDS/pg_dump 백업을 계속 사용하세요.

읽기 토큰으로 백업합니다(예: cron).

```bash
curl -fsS -H "Authorization: Bearer $INFERPLANED_TOKEN" \
  "$CP/v1alpha1/config/export" -o "policies-$(date -u +%Y%m%dT%H%M%SZ).yaml"
```

복원할 때는 먼저 미리보기를 실행하고, 미리보기에서 받은 generation으로 적용합니다. 정책 쓰기 토큰을 사용합니다.

```bash
curl -fsS -X POST -H "Authorization: Bearer $INFERPLANED_POLICY_WRITE_TOKEN" \
  --data-binary @policies.yaml "$CP/v1alpha1/config/import?dry_run=true" | tee preview.json
GEN=$(jq -r .current_generation preview.json)
curl -fsS -X POST -H "Authorization: Bearer $INFERPLANED_POLICY_WRITE_TOKEN" \
  --data-binary @policies.yaml "$CP/v1alpha1/config/import?expected_generation=$GEN"
```

가져오기는 정책 세트 전체를 교체합니다. 파일에 없는 문서는 삭제됩니다. `409`가 나오면 미리보기 이후 정책이 바뀐 것이니 미리보기를 다시 실행하세요. 빈 파일은 `allow_empty=true`가 필요합니다. 파일은 최대 64 MiB이며, 데이터 플레인이 받을 수 있는 8 MiB JSON보다 큰 세트는 `400`으로 거부됩니다. "committed but not applied" `500`이 나오면 store에는 이미 새 세트가 반영된 것이니, 미리보기를 다시 실행해 실제로 배포 중인 세트를 확인하세요. 콘솔의 Policies → Export YAML / Import (restore)에서도 같은 흐름을 제공합니다.

이 변경 이전에 준비한 배포에서는 `/v1alpha1/config/*` 요청이 여전히 기존 CP origin으로 갑니다. `prepare-policy-controlplane.py`는 이미 policy origin이 있으면 실행을 거부하므로, `/v1alpha1/config/*` behavior(policy-cp origin, `forward-auth` origin request policy)를 검토를 거쳐 수동으로 추가하세요. 그다음 policy/SSO/config 경로 6개를 무효화합니다.
````

- [ ] **Step 8: Route the config endpoints to the policy CP**

In `scripts/prepare-policy-controlplane.py`, extend `PATHS`:

```python
PATHS = (
    "/v1alpha1/policies",
    "/v1alpha1/policies/*",
    "/v1alpha1/config/*",
    "/v1alpha1/sync",
    "/v1alpha1/dataplanes",
    "/ui/auth/config",
)
```

In `tests/deployment/test_policy_store.py` `test_preserves_all_unrelated_resources_and_request_authorization`, replace both hardcoded `[:5]` slices (the `PathPattern` assertion and the `for behavior in …` loop, lines 49–50) with `[:len(helper.PATHS)]`, and add:

```python
        self.assertIn("/v1alpha1/config/*", helper.PATHS)
```

In `docs/runbooks/inferplaned-policy-store.md:63`, change "Invalidate the five policy/SSO paths." to "Invalidate the six policy/SSO/config paths.". In `docs/runbooks/inferplaned-policy-store.ko.md:63`, change `정책·SSO 경로 5개를 invalidate합니다.` to `정책·SSO·config 경로 6개를 invalidate합니다.`.

- [ ] **Step 9: Update module notes**

Append to the `controlplane/` bullet in `internal/CLAUDE.md`:

```markdown
 `import.go` (`POST /v1alpha1/config/import`) is replace-mode backup restore: it validates the WHOLE bundle before any store write (the reverse of `ApplyWrite`'s commit-then-reload), reconciles with the store first (preview and apply), diffs canonical re-marshals by name, requires `expected_generation` checked under `writeMu` — the mutex every store writer (`ApplyWrite`/`ApplyDelete`/`ApplyImport`) holds across commit+install; lock order `writeMu` before `mu` — and installs the validated snapshot via `applyWire` after commit (no DB re-read). Size contract: 64 MiB YAML transport cap; the parsed set's JSON must fit the 8 MiB data-plane sync decode bound. A Replace error is read back (lost-ack ⇒ confirmed commit).
```

- [ ] **Step 10: Run the tests and full repository verification**

```bash
mkdir -p /tmp/claude-1001
go test ./internal/controlplane/ -race > /tmp/claude-1001/cptest.log 2>&1; echo rc=$?; grep -E "^(--- FAIL|FAIL|ok)" /tmp/claude-1001/cptest.log
python3 -m unittest tests/deployment/test_policy_store.py > /tmp/claude-1001/deploytest.log 2>&1; echo rc=$?; tail -3 /tmp/claude-1001/deploytest.log
CGO_ENABLED=0 go build -trimpath -o /tmp/claude-1001/mayu ./cmd/mayu && CGO_ENABLED=0 go build -trimpath -o /tmp/claude-1001/inferplaned ./cmd/inferplaned; echo build=$?
go test ./... -race > /tmp/claude-1001/all.log 2>&1; echo rc=$?; grep -E "^(--- FAIL|FAIL)" /tmp/claude-1001/all.log
go vet ./... ; gofmt -l . | grep -v '^\.worktrees/\|^\.claude/'
bash tests/run-all.sh > /tmp/claude-1001/harness.log 2>&1; echo rc=$?; tail -5 /tmp/claude-1001/harness.log
```

Expected:
- every `rc=0` and `build=0`;
- `ok  github.com/inferplane/inferplane/internal/controlplane`, with all `TestImport*` passing and the pre-existing tests (`TestConfigExportRoundTrips`, `TestPolicyHTTPMapping`) still passing;
- no `FAIL` lines;
- no `go vet` or `gofmt` output.

- [ ] **Step 11: Commit**

```bash
git add internal/controlplane/import.go internal/controlplane/import_test.go internal/controlplane/policies.go internal/controlplane/controlplane.go internal/controlplane/export.go scripts/prepare-policy-controlplane.py tests/deployment/test_policy_store.py docs/reference/api.md docs/reference/api.ko.md docs/runbooks/inferplaned-policy-store.md docs/runbooks/inferplaned-policy-store.ko.md internal/CLAUDE.md
git commit -s -m "feat(controlplane): dry-run-first replace import for policy backup/restore"
```


---

### Task 3: Console Export / Import buttons

**Files:**
- Modify: `internal/controlplane/ui/static/index.html`
- Modify: `internal/controlplane/ui/static/app.js`
- Test: `internal/controlplane/ui/testdata/policy_import.cjs`
- Modify: `internal/controlplane/ui/dashboard_test.go`
- Modify: `docs/runbooks/inferplaned-dashboard.md`
- Modify: `docs/runbooks/inferplaned-dashboard.ko.md`

**Interfaces:**
- Consumes: the HTTP contract produced by Task 2 (mocked in the test via `boot({ custom })`). From `testdata/dashboard.cjs` it uses `boot(options)` returning `{ $, calls, writes, policy, setCustom, connect(), credential() }`, plus `flush`. `options.custom(url, init, resp)` intercepts any request first.
- Produces: DOM ids `export-policies`, `import-policies`, `import-file`. In `app.js`, `request()` gains the options `raw` (a string body sent verbatim as `application/yaml`) and `text` (return the response text instead of parsed JSON). There is also a new `pendingChange.mode === "import"`.

- [ ] **Step 1: Write the failing test**

Create `internal/controlplane/ui/testdata/policy_import.cjs`:

```js
// Policy export/import (backup/restore) through the real UI handlers.
const assert = require('node:assert/strict');
const { boot } = require('./dashboard.cjs');
process.exitCode = 1;

const bundle = 'apiVersion: inferplane.dev/v1alpha1\nkind: GovernancePolicy\nmetadata: { name: restored }\nspec:\n  subject: { team: t }\n  rules:\n  - name: models\n    failurePolicy: FailOpen\n    modelAccess: { allow: ["*"] }\n';
const preview = { dry_run: true, current_generation: 'g2', result_generation: 'g3',
  changes: [{ name: 'engineering', action: 'removed' }, { name: 'restored', action: 'added' }],
  summary: { added: 1, changed: 0, removed: 1, unchanged: 0 } };
const file = text => ({ size: Buffer.byteLength(text), text: async () => text });

function importRoutes(state) {
  return async (url, init, resp) => {
    if (url === '/v1alpha1/config/export') { state.exported = (state.exported || 0) + 1; return { status: 200, ok: true, text: async () => bundle, json: async () => { throw Error('not json'); } }; }
    if (!url.startsWith('/v1alpha1/config/import')) return null;
    state.imports.push({ url, init });
    const q = new URLSearchParams(url.split('?')[1]);
    if (q.get('dry_run') === 'true') return resp(200, state.preview || preview);
    if (state.conflict) return resp(409, { error: 'policies changed since preview', current_generation: 'g9' });
    return resp(200, { ...(state.preview || preview), dry_run: false });
  };
}

(async () => {
  // Read-only session: export available, import hidden/disabled.
  let state = { imports: [] };
  let app = boot({ custom: importRoutes(state) }); await app.connect(); await app.$('nav-policies').fire('click');
  assert.equal(app.$('export-policies').disabled, false, 'export needs only read access');
  assert.equal(app.$('import-policies').disabled, true, 'read token enabled import');
  await app.$('import-policies').fire('click');
  app.$('import-file').files = [file(bundle)]; await app.$('import-file').fire('change');
  assert.equal(state.imports.length, 0, 'import handler bypassed read-only controls');

  // File-authoritative deployment: import button hidden entirely.
  app = boot({ writable: false, custom: importRoutes({ imports: [] }) }); await app.connect(); await app.credential();
  assert.equal(app.$('import-policies').hidden, true, 'import shown without a policy store');

  // Export downloads the server YAML verbatim.
  state = { imports: [] };
  app = boot({ custom: importRoutes(state) }); await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  await app.$('export-policies').fire('click');
  assert.equal(state.exported, 1);
  const exportCall = app.calls.find(c => c.url === '/v1alpha1/config/export');
  assert.equal(exportCall.init.headers.Authorization, 'Bearer read-secret', 'export must use the read credential');
  const link = app.document.body.querySelectorAll('a').find(a => /^inferplane-policies-g2.*\.yaml$/.test(a.download || ''));
  assert(link, 'no download link was created for the export');

  // Import: dry-run preview first, nothing applied yet.
  assert.equal(app.$('import-policies').disabled, false);
  app.$('import-file').files = [file(bundle)];
  await app.$('import-file').fire('change');
  assert.equal(state.imports.length, 1, 'preview was not requested');
  assert.match(state.imports[0].url, /dry_run=true/);
  assert.equal(state.imports[0].init.method, 'POST');
  assert.equal(state.imports[0].init.body, bundle, 'bundle was not sent verbatim');
  assert.equal(state.imports[0].init.headers['Content-Type'], 'application/yaml');
  assert.equal(state.imports[0].init.headers.Authorization, 'Bearer write-secret');
  assert.equal(app.$('policy-dialog').open, true);
  assert.match(app.$('policy-review').textContent, /1 added · 0 changed · 1 removed · 0 unchanged/);
  assert.match(app.$('policy-review').textContent, /removed\s+engineering/);
  assert.match(app.$('dialog-confirm').textContent, /delete 1 policy/i, 'removal count not surfaced on confirm');

  // Confirm applies with the previewed generation.
  await app.$('dialog-confirm').fire('click');
  assert.equal(state.imports.length, 2);
  const apply = new URLSearchParams(state.imports[1].url.split('?')[1]);
  assert.equal(apply.get('expected_generation'), 'g2');
  assert.equal(apply.get('dry_run'), null);
  assert.equal(apply.get('allow_empty'), null);
  assert.equal(app.$('policy-dialog').open, false, 'dialog stayed open after restore');
  assert.match(app.$('notice').textContent, /restored/i);

  // 409: dialog stays open with a re-preview instruction.
  state = { imports: [], conflict: true };
  app = boot({ custom: importRoutes(state) }); await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  app.$('import-file').files = [file(bundle)]; await app.$('import-file').fire('change');
  await app.$('dialog-confirm').fire('click');
  assert.equal(app.$('policy-dialog').open, true);
  assert.match(app.$('dialog-error').textContent, /changed since preview; re-run preview/i);

  // Empty bundle: explicit warning and allow_empty on apply.
  state = { imports: [], preview: { dry_run: true, current_generation: 'g2', result_generation: 'g0', changes: [{ name: 'engineering', action: 'removed' }], summary: { added: 0, changed: 0, removed: 1, unchanged: 0 } } };
  app = boot({ custom: importRoutes(state) }); await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  app.$('import-file').files = [file('')]; await app.$('import-file').fire('change');
  assert.match(app.$('dialog-error').textContent, /deletes every policy/i);
  await app.$('dialog-confirm').fire('click');
  assert.equal(new URLSearchParams(state.imports[1].url.split('?')[1]).get('allow_empty'), 'true');

  // Oversized file never leaves the browser.
  state = { imports: [] };
  app = boot({ custom: importRoutes(state) }); await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  app.$('import-file').files = [{ size: (64 << 20) + 1, text: async () => 'x' }]; await app.$('import-file').fire('change');
  assert.equal(state.imports.length, 0, 'oversized bundle was uploaded');
  assert.match(app.$('error').textContent, /64 MiB/);

  // A refused WRITE credential keeps the read session (dashboard.cjs write-403 contract).
  state = { imports: [] };
  app = boot({ custom: async (url, init, resp) => url.startsWith('/v1alpha1/config/import') ? resp(403, { error: 'forbidden' }) : importRoutes(state)(url, init, resp) });
  await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  app.$('import-file').files = [file(bundle)]; await app.$('import-file').fire('change');
  assert.equal(app.$('policy-dialog').open, false, 'preview dialog opened after 403');
  assert.equal(app.$('workspace-content').hidden, false, 'a write refusal discarded the read session');
  assert.match(app.$('error').textContent, /write refused/i);

  // A file read that resolves after disconnect never uploads with the next session.
  state = { imports: [] };
  app = boot({ custom: importRoutes(state) }); await app.connect(); await app.credential(); await app.$('nav-policies').fire('click');
  let finishRead; app.$('import-file').files = [{ size: 10, text: () => new Promise(r => { finishRead = () => r(bundle); }) }];
  const pending = app.$('import-file').fire('change');
  await app.$('disconnect').fire('click');
  assert.equal(app.$('import-policies').disabled, true, 'import stayed enabled after disconnect');
  assert.equal(app.$('export-policies').disabled, true, 'export stayed enabled after disconnect');
  await app.connect(); await app.credential();
  finishRead(); await pending;
  assert.equal(state.imports.length, 0, 'a stale-session bundle was uploaded with the new credential');

  process.exitCode = 0;
})().catch(e => { console.error(e); process.exitCode = 1; });
```

Append to `internal/controlplane/ui/dashboard_test.go`:

```go
func TestPolicyImportWorkflow(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("policy import checks need Node (stdlib only)")
	}
	out, err := exec.Command(node, "testdata/policy_import.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("policy import regression: %v\n%s", err, out)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/controlplane/ui/ -run TestPolicyImportWorkflow -v`
Expected: FAIL with `unknown DOM id export-policies` from the harness.

- [ ] **Step 3: Add the controls to `index.html`**

In `internal/controlplane/ui/static/index.html`, inside `<div class="heading-actions">` of the Policies `section-toolbar`, insert before `<button id="new-rule"`:

```html
<button id="export-policies" class="button secondary" disabled>Export YAML</button><button id="import-policies" class="button secondary" disabled>Import (restore)</button><input id="import-file" type="file" accept=".yaml,.yml,.json,application/yaml,application/json" hidden>
```

- [ ] **Step 4: Extend `request()` in `app.js`**

Change the signature at `app.js` `async function request(`:

```js
  async function request(path, { method = "GET", body, raw, text = false, write = false, byteLimit = 0 } = {}) {
```

Replace the `headers:` and `body:` lines in the `fetch(path, { … })` call with:

```js
        headers: { ...(auth ? { Authorization: "Bearer " + auth } : {}), ...(raw !== undefined ? { "Content-Type": "application/yaml" } : body !== undefined ? { "Content-Type": "application/json" } : {}) },
        body: raw !== undefined ? raw : body !== undefined ? exactJSON(body) : undefined,
```

Directly before the `let raw;` declaration that follows the `!resp.ok` block, add:

```js
      if (text) return resp.text();
```

Rename that later local `let raw;` and its uses inside `send()` to `let payload;` so it does not shadow the new `raw` option. Its uses are: `raw = parts.join("")`, `raw = await resp.text()`, the `byteLimit` check, and `return parseExactJSON(raw)`.

- [ ] **Step 5: Wire the buttons in `app.js`**

In `renderPolicies()`, after `$("new-rule").disabled = !canWrite();`, add:

```js
    $("export-policies").disabled = !policies;
    $("import-policies").hidden = !writable; $("import-policies").disabled = !canWrite();
```

Add `openImport` after `openDelete`:

```js
  function openImport(bundle, preview) {
    if (!canWrite()) return;
    const s = preview.summary, empty = s.added + s.changed + s.unchanged === 0;
    pendingChange = { mode: "import", bundle, preview, empty, session, wasDirty: dirty };
    dialogBase("Restore · preview", "Importing REPLACES the complete policy set with this file. Documents absent from the file are deleted. Budget ledger balances and usage history are not restored.");
    $("policy-review").textContent = [
      "Summary: " + s.added + " added · " + s.changed + " changed · " + s.removed + " removed · " + s.unchanged + " unchanged",
      "Current generation: " + preview.current_generation,
      "Result generation:  " + preview.result_generation, "",
      ...preview.changes.map(c => c.action.padEnd(10) + " " + c.name),
    ].join("\n");
    if (empty) message("dialog-error", "This file contains no policies. Applying it deletes every policy.");
    $("dialog-confirm").textContent = s.removed > 0 ? "Restore and delete " + s.removed + " " + (s.removed === 1 ? "policy" : "policies") : "Restore policies";
  }
```

In the `$("dialog-confirm")` click handler, insert this block directly before the line `if (change.mode === "delete" && $("policy-editor").value !== change.name)`:

```js
    if (change.mode === "import") {
      $("dialog-confirm").disabled = $("dialog-cancel").disabled = $("dialog-close").disabled = $("dialog-back").disabled = true;
      try {
        const q = new URLSearchParams({ expected_generation: change.preview.current_generation });
        if (change.empty) q.set("allow_empty", "true");
        const result = await request("/v1alpha1/config/import?" + q, { method: "POST", raw: change.bundle, write: true });
        if (change.session !== session) return;
        $("dialog-confirm").disabled = false; closeDialog(); dirty = false;
        await refresh();
        if (change.session === session && connected) {
          const s = result.summary;
          message("notice", "Policies restored: " + s.added + " added, " + s.changed + " changed, " + s.removed + " removed. Check data-plane generation coverage after the next heartbeat. ");
          const inspect = el("button", "Inspect distribution →", "text-button"); inspect.addEventListener("click", () => inspectFleet()); $("notice").append(inspect);
        }
      } catch (e) { if (change.session === session) message("dialog-error", e.status === 409 ? "Policies changed since preview; re-run preview." : e.message); }
      finally { if (change.session === session) $("dialog-confirm").disabled = $("dialog-cancel").disabled = $("dialog-close").disabled = $("dialog-back").disabled = false; }
      return;
    }
```

After the existing `$("export-usage").addEventListener(...)` block, add:

```js
  $("export-policies").addEventListener("click", async () => {
    if (!connected) return;
    const sid = session; $("export-policies").disabled = true; setError("");
    try {
      const yaml = await request("/v1alpha1/config/export", { text: true });
      if (sid !== session) return;
      const blob = new Blob([yaml], { type: "application/yaml" }), url = URL.createObjectURL(blob), link = el("a");
      link.href = url; link.download = "inferplane-policies-" + String(policies?.generation || "export").slice(0, 12) + ".yaml";
      document.body.append(link); link.click(); link.remove(); URL.revokeObjectURL(url);
      message("notice", "Exported the distributed policy set. Restore it with Import (restore).");
    } catch (e) { if (sid === session) { if (e.status === 401 || e.status === 403) lock(); setError(e.message); } }
    finally { if (sid === session) $("export-policies").disabled = !policies; }
  });
  $("import-policies").addEventListener("click", () => {
    if (!canWrite()) return;
    $("import-file").value = ""; $("import-file").click();
  });
  $("import-file").addEventListener("change", async () => {
    const file = $("import-file").files?.[0]; if (!file || !canWrite()) return;
    const sid = session; setError("");
    if (file.size > 67108864) { setError("Import file exceeds 64 MiB."); return; }
    try {
      const bundle = await file.text();
      if (sid !== session || !canWrite()) return; // the read outlived its session
      const preview = await request("/v1alpha1/config/import?dry_run=true", { method: "POST", raw: bundle, write: true });
      if (sid !== session) return;
      openImport(bundle, preview);
    } catch (e) { if (sid === session) setError(e.message); } // write refusals keep the read session, like PUT/DELETE
  });
```

In `clearData()` (`app.js:192`), add next to the other control resets:

```js
    $("export-policies").disabled = $("import-policies").disabled = true;
```

Also update the Policies row of the console runbooks (same-commit docs rule).
- In `docs/runbooks/inferplaned-dashboard.md:26`, append to the row's capability list: `, Export YAML download and Import (restore): dry-run diff first, then a replace-mode apply pinned to the previewed generation (64 MiB file cap; budget ledger and usage are not restored)`.
- In `docs/runbooks/inferplaned-dashboard.ko.md:30`, append: `, YAML 내보내기와 가져오기(복원): dry-run diff를 먼저 보여 주고, 미리보기 generation으로 고정해 Replace 방식으로 적용합니다(파일 최대 64 MiB, 예산 원장·사용량은 복원하지 않음)`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/controlplane/ui/ -race > /tmp/claude-1001/uitest.log 2>&1; echo rc=$?; grep -E "^(--- FAIL|FAIL|ok)" /tmp/claude-1001/uitest.log`
Expected: `rc=0`. `TestPolicyImportWorkflow` passes, and every existing cjs suite (dashboard, charts, budgets, workspace, user_limits, sso_verifier) still passes.

- [ ] **Step 7: Commit**

```bash
git add internal/controlplane/ui/static/index.html internal/controlplane/ui/static/app.js internal/controlplane/ui/testdata/policy_import.cjs internal/controlplane/ui/dashboard_test.go docs/runbooks/inferplaned-dashboard.md docs/runbooks/inferplaned-dashboard.ko.md
git commit -s -m "feat(cp-console): policy export download and dry-run-first import restore"
```
