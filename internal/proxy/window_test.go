package proxy

// Window-epoch tests, data-plane side (roadmap ② second half): the control
// plane owns the budget window and stamps every grant with its epoch id;
// mayu's LOCAL counter rolls at the operator-timezone calendar boundary, so
// on an observed epoch CHANGE the syncer baselines the counter and reports
// (counter − baseline) — old-window spend never books into the fresh window,
// in either direction of the timezone skew.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
)

const monthOnlyPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-a }
spec:
  subject: { team: alpha }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 1000000
      hardCap: true
      lease: { grantMilliUSD: 1000, renewInterval: "5s" }
`

func TestSyncerEpochChangeBaselinesLocalCounter(t *testing.T) {
	cp := newRecordingCP(t)
	store := policy.NewEmptyStore()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(monthOnlyPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	wire, _, err := policy.LoadWirePaths(dir)
	if err != nil {
		t.Fatalf("LoadWirePaths: %v", err)
	}
	if rej := store.ApplyWire(wire); len(rej) != 0 {
		t.Fatalf("policy rejected: %+v", rej)
	}

	grant := func(windowID string) []policy.LeaseGrant {
		return []policy.LeaseGrant{{
			Policy: "team-a", Rule: "cap", Team: "alpha",
			AllowanceMicroUSD: 1_000_000, ExpiresAt: time.Now().Add(time.Minute),
			HardCap: true, Period: v1alpha1.PeriodCalendarMonth, WindowID: windowID,
		}}
	}

	var localSpend int64
	s := &Syncer{
		URL: cp.srv.URL, Dataplane: "dp1", Store: store, Leases: NewLeaseTable(),
		SpentOf: func(string, v1alpha1.BudgetPeriod) int64 { return localSpend },
	}
	sync := func() policy.ConsumptionReport {
		t.Helper()
		if _, err := s.syncOnce(context.Background()); err != nil {
			t.Fatalf("syncOnce: %v", err)
		}
		if len(cp.last.Reports) != 1 {
			t.Fatalf("want 1 report, got %+v", cp.last.Reports)
		}
		return cp.last.Reports[0]
	}

	// Heartbeat 1: no lease yet, so the report carries no epoch and the raw
	// counter — exactly the pre-epoch wire.
	cp.resp.Leases = grant("2027-03")
	localSpend = 500
	if rep := sync(); rep.WindowID != "" || rep.SpentMicroUSD != 500 {
		t.Fatalf("first report = %+v, want raw 500 with no epoch", rep)
	}

	// Heartbeat 2, same epoch: report is stamped, baseline stays 0.
	localSpend = 800
	if rep := sync(); rep.WindowID != "2027-03" || rep.SpentMicroUSD != 800 {
		t.Fatalf("in-epoch report = %+v, want 800 @ 2027-03", rep)
	}

	// The control plane's window rolls; the LOCAL counter has not (operator
	// timezone behind UTC). Heartbeat 3 still reports the old epoch (built
	// before the new grant lands), then baselines at 800.
	cp.resp.Leases = grant("2027-04")
	if rep := sync(); rep.WindowID != "2027-03" || rep.SpentMicroUSD != 800 {
		t.Fatalf("rollover-heartbeat report = %+v, want the old epoch's 800", rep)
	}
	if l, _ := s.Leases.Get("alpha", v1alpha1.PeriodCalendarMonth); l.WindowID != "2027-04" || l.BaselineMicroUSD != 800 {
		t.Fatalf("epoch change must baseline the counter: %+v", l)
	}

	// Heartbeat 4: new spend reports relative to the baseline.
	localSpend = 1_000
	if rep := sync(); rep.WindowID != "2027-04" || rep.SpentMicroUSD != 200 {
		t.Fatalf("baselined report = %+v, want 1000-800=200 @ 2027-04", rep)
	}

	// The LOCAL boundary passes: the counter rolls below the baseline. The
	// baseline's spend no longer exists in the counter, so it resets and
	// the raw value is window-correct again.
	localSpend = 50
	if rep := sync(); rep.WindowID != "2027-04" || rep.SpentMicroUSD != 50 {
		t.Fatalf("post-local-roll report = %+v, want raw 50", rep)
	}
	if l, _ := s.Leases.Get("alpha", v1alpha1.PeriodCalendarMonth); l.BaselineMicroUSD != 0 {
		t.Fatalf("local roll must reset the baseline: %+v", l)
	}
}
