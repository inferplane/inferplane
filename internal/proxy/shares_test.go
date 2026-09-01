package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/controlplane"
	"github.com/inferplane/inferplane/internal/policy"
)

func TestShareTableMostRestrictiveFoldAndKeepLast(t *testing.T) {
	tbl := NewShareTable()

	// Two rules for one team in one batch: min per dimension wins, and a
	// dimension only one rule shares survives the fold.
	tbl.Set([]policy.RateShare{
		{Policy: "p", Rule: "r1", Team: "alpha", RPM: 150, TPM: 3000},
		{Policy: "p", Rule: "r2", Team: "alpha", RPM: 100},
		{Policy: "p", Rule: "r3", Team: "beta", TPM: 500},
	})
	sh, ok := tbl.Get("alpha")
	if !ok || sh.RPM != 100 || sh.TPM != 3000 {
		t.Fatalf("alpha fold = %+v, %v; want RPM 100 (most restrictive), TPM 3000 (only declarer)", sh, ok)
	}
	if sh, ok := tbl.Get("beta"); !ok || sh.RPM != 0 || sh.TPM != 500 {
		t.Fatalf("beta = %+v, %v", sh, ok)
	}

	// A later batch mentioning only beta leaves alpha's share standing
	// (keep-last, FailOpen), and an empty batch changes nothing.
	tbl.Set([]policy.RateShare{{Policy: "p", Rule: "r3", Team: "beta", TPM: 250}})
	tbl.Set(nil)
	if sh, _ := tbl.Get("alpha"); sh.RPM != 100 {
		t.Fatalf("alpha share must survive batches that don't mention it: %+v", sh)
	}
	if sh, _ := tbl.Get("beta"); sh.TPM != 250 {
		t.Fatalf("beta share must follow the newest batch: %+v", sh)
	}

	// A grown share (a plane left the fleet) takes effect immediately —
	// the fold is per batch, never against the stored value.
	tbl.Set([]policy.RateShare{{Policy: "p", Rule: "r1", Team: "alpha", RPM: 300, TPM: 6000}})
	if sh, _ := tbl.Get("alpha"); sh.RPM != 300 || sh.TPM != 6000 {
		t.Fatalf("grown share must replace the stored one: %+v", sh)
	}
}

// TestSyncerAppliesSharesAndKeepsThemThroughOutage drives the real control
// plane: shares land with the heartbeat, and a control-plane outage keeps
// the last shares in force (never widening back to the global limit).
func TestSyncerAppliesSharesAndKeepsThemThroughOutage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(`apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-a }
spec:
  subject: { team: alpha }
  rules:
  - name: throttle
    failurePolicy: FailOpen
    rate: { rpm: 300, tpm: 6000 }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer("", dir)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	cp.Mount(mux)
	ts := httptest.NewServer(mux)

	shares := NewShareTable()
	s := &Syncer{
		URL: ts.URL, Dataplane: "dp1",
		Store: policy.NewEmptyStore(), Leases: NewLeaseTable(), Shares: shares,
		SpentOf: func(string, v1alpha1.BudgetPeriod) int64 { return 0 },
	}
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	sh, ok := shares.Get("alpha")
	if !ok || sh.RPM != 300 || sh.TPM != 6000 {
		t.Fatalf("single plane share = %+v, %v", sh, ok)
	}

	// A second plane registers; this plane's next heartbeat halves its share.
	if _, err := (&Syncer{
		URL: ts.URL, Dataplane: "dp2",
		Store: policy.NewEmptyStore(), Leases: NewLeaseTable(),
		SpentOf: func(string, v1alpha1.BudgetPeriod) int64 { return 0 },
	}).syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sh, _ := shares.Get("alpha"); sh.RPM != 150 || sh.TPM != 3000 {
		t.Fatalf("share after second plane joined = %+v, want 150/3000", sh)
	}

	// Control-plane outage: the failed heartbeat must leave the last share
	// standing — never widen back to 300, never zero.
	ts.Close()
	if _, err := s.syncOnce(context.Background()); err == nil {
		t.Fatal("sync against a dead control plane must error")
	}
	if sh, ok := shares.Get("alpha"); !ok || sh.RPM != 150 || sh.TPM != 3000 {
		t.Fatalf("share must survive an outage unchanged: %+v, %v", sh, ok)
	}
}
