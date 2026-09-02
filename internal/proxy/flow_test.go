package proxy

// ADR-043 second half, data-plane side: the syncer differentiates the
// governor's cumulative settled-traffic counters per heartbeat and
// EWMA-smooths them into the recent per-minute flow the control plane's
// proportional rate split consumes.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/policy"
)

const rateOnlyPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-a }
spec:
  subject: { team: alpha }
  rules:
  - name: throttle
    failurePolicy: FailOpen
    rate: { rpm: 300, tpm: 6000 }
`

func TestSyncOnceReportsEWMAFlow(t *testing.T) {
	cp := newRecordingCP(t)
	store := policy.NewEmptyStore()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(rateOnlyPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	wire, _, err := policy.LoadWirePaths(dir)
	if err != nil {
		t.Fatalf("LoadWirePaths: %v", err)
	}
	if rej := store.ApplyWire(wire); len(rej) != 0 {
		t.Fatalf("policy rejected: %+v", rej)
	}

	var reqs, tokens int64
	clock := time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)
	s := &Syncer{
		URL: cp.srv.URL, Dataplane: "dp1", Store: store,
		FlowOf: func(team string) (int64, int64) {
			if team != "alpha" {
				t.Errorf("FlowOf team = %q, want alpha", team)
			}
			return reqs, tokens
		},
		nowFn: func() time.Time { return clock },
	}
	sync := func() []policy.TeamFlow {
		t.Helper()
		if _, err := s.syncOnce(context.Background()); err != nil {
			t.Fatalf("syncOnce: %v", err)
		}
		return cp.last.Flows
	}

	// Heartbeat 1 establishes the baseline: a rate needs two samples.
	reqs, tokens = 100, 2000
	if flows := sync(); len(flows) != 0 {
		t.Fatalf("first heartbeat must report no flow, got %+v", flows)
	}

	// One minute later, 60 requests / 1200 tokens more: instantaneous
	// 60 rpm / 1200 tpm, EWMA from 0 → half of that.
	clock = clock.Add(time.Minute)
	reqs, tokens = 160, 3200
	flows := sync()
	if len(flows) != 1 || flows[0].Team != "alpha" || flows[0].RPM != 30 || flows[0].TPM != 600 {
		t.Fatalf("second heartbeat flows = %+v, want alpha 30 rpm / 600 tpm (EWMA half of 60/1200)", flows)
	}

	// Steady state converges toward the true rate.
	clock = clock.Add(time.Minute)
	reqs, tokens = 220, 4400
	if flows := sync(); flows[0].RPM != 45 || flows[0].TPM != 900 {
		t.Fatalf("third heartbeat flows = %+v, want 45/900 (EWMA converging on 60/1200)", flows)
	}

	// A counter restart (process bounce) never reports a negative rate —
	// it re-baselines and decays instead.
	clock = clock.Add(time.Minute)
	reqs, tokens = 5, 100
	if flows := sync(); flows[0].RPM < 0 || flows[0].RPM > 45 {
		t.Fatalf("post-restart flows = %+v, want a decayed non-negative rpm", flows)
	}
}
