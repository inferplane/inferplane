package controlplane

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/policy"
)

// Roadmap ② durability half: with INFERPLANED_LEDGER_PATH's store attached,
// a control-plane restart resumes the lease ledger exactly — a dead or
// not-yet-reheard-from plane's spend stays on the books instead of being
// re-learned (or lost) from later cumulative reports.

func newLedgerServer(t *testing.T, polDir, dbPath string) (*Server, *httptest.Server) {
	t.Helper()
	s, err := NewServer("", polDir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ls, err := NewSQLiteLedger(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteLedger: %v", err)
	}
	t.Cleanup(func() { ls.Close() })
	if err := s.SetLedgerStore(ls); err != nil {
		t.Fatalf("SetLedgerStore: %v", err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

func TestLedgerStoreRestartPreservesSpendAndOutstanding(t *testing.T) {
	polDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(cpPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "ledger.db")

	// Life before the restart: dp1 has spent 95 of the 100-milliUSD limit
	// (95,000 of 100,000 µUSD) and holds the remaining 5,000 as its grant.
	_, ts1 := newLedgerServer(t, polDir, dbPath)
	resp := doSync(t, ts1.URL, "", policy.SyncRequest{
		Dataplane: "dp1",
		Reports:   []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 95_000}},
	})
	if len(resp.Leases) != 1 || resp.Leases[0].AllowanceMicroUSD != 100_000 {
		t.Fatalf("dp1 pre-restart lease = %+v, want allowance 100000 (spent 95000 + remaining 5000)", resp.Leases)
	}
	ts1.Close()

	// Restart: a FRESH server over the same policy dir and the same ledger
	// file. Without the store this server would know nothing of dp1.
	_, ts2 := newLedgerServer(t, polDir, dbPath)

	// dp2's first-ever heartbeat, before dp1 has re-reported anything: the
	// global budget is fully committed (95,000 spent + 5,000 outstanding to
	// dp1), so dp2's grant must be zero — the in-memory-only server would
	// have handed it 10,000 µUSD of already-spent money.
	resp = doSync(t, ts2.URL, "", policy.SyncRequest{Dataplane: "dp2"})
	if len(resp.Leases) != 1 {
		t.Fatalf("dp2 leases = %+v, want 1", resp.Leases)
	}
	if got := resp.Leases[0].AllowanceMicroUSD; got != 0 {
		t.Fatalf("dp2 post-restart allowance = %d, want 0 — restart resurrected spend the store should have preserved", got)
	}
	if !resp.Leases[0].HardCap {
		t.Fatal("hard cap flag lost across restart")
	}
}

func TestLedgerStorePruneDeletesRows(t *testing.T) {
	polDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(cpPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "ledger.db")

	s, ts := newLedgerServer(t, polDir, dbPath)
	doSync(t, ts.URL, "", policy.SyncRequest{
		Dataplane: "dp1",
		Reports:   []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 40_000}},
	})

	// dp1 goes silent past the 24h prune horizon; dp2's heartbeat triggers
	// the prune, which must also delete dp1's persisted rows.
	base := time.Now()
	s.mu.Lock()
	s.now = func() time.Time { return base.Add(25 * time.Hour) }
	s.mu.Unlock()
	doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp2"})
	ts.Close()

	// A fresh server over the same file must not know dp1 anymore.
	s2, ts2 := newLedgerServer(t, polDir, dbPath)
	defer ts2.Close()
	s2.mu.Lock()
	_, dp1Known := s2.dataplanes["dp1"]
	var dp1Spent int64
	for _, l := range s2.ledger {
		dp1Spent += l.spent["dp1"]
	}
	s2.mu.Unlock()
	if dp1Known || dp1Spent != 0 {
		t.Fatalf("pruned dp1 must not survive in the store (known=%v spent=%d)", dp1Known, dp1Spent)
	}
}

func TestLedgerStoreIgnoresRowsForChangedPeriodOrRemovedRule(t *testing.T) {
	polDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(cpPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "ledger.db")

	_, ts := newLedgerServer(t, polDir, dbPath)
	doSync(t, ts.URL, "", policy.SyncRequest{
		Dataplane: "dp1",
		Reports:   []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 40_000}},
	})
	ts.Close()

	// The rule's window changes (month → day): its persisted rows are a
	// different currency and must not restore.
	edited := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-a }
spec:
  subject: { team: alpha }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget:
      period: CalendarDay
      limitMilliUSD: 100
      hardCap: true
      lease: { grantMilliUSD: 10, renewInterval: "5s" }
`
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, ts2 := newLedgerServer(t, polDir, dbPath)
	defer ts2.Close()
	s2.mu.Lock()
	var restored int64
	for _, l := range s2.ledger {
		restored += l.spent["dp1"]
	}
	s2.mu.Unlock()
	if restored != 0 {
		t.Fatalf("a row measured against CalendarMonth restored into a CalendarDay rule: %d", restored)
	}
}
