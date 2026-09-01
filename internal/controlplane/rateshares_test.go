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

// ADR-043: the control plane divides each team rate rule's global rpm/tpm
// equally among live data planes, so the fleet aggregate is bounded by the
// configured limit instead of N× it.

const rateSharePolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-a }
spec:
  subject: { team: alpha }
  rules:
  - name: throttle
    failurePolicy: FailOpen
    rate: { rpm: 300, tpm: 6000 }
  - name: nocap
    failurePolicy: FailOpen
    rate: { unlimited: true }
`

func newRateShareServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(rateSharePolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer("", dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

func shareFor(t *testing.T, resp policy.SyncResponse, rule string) policy.RateShare {
	t.Helper()
	for _, sh := range resp.RateShares {
		if sh.Rule == rule {
			return sh
		}
	}
	t.Fatalf("no share for rule %q in %+v", rule, resp.RateShares)
	return policy.RateShare{}
}

func TestRateSharesSplitEquallyAmongLivePlanes(t *testing.T) {
	_, ts := newRateShareServer(t)

	// One plane: the whole limit.
	resp := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	sh := shareFor(t, resp, "throttle")
	if sh.Team != "alpha" || sh.RPM != 300 || sh.TPM != 6000 {
		t.Fatalf("single plane must hold the full limit: %+v", sh)
	}

	// A second plane joins: each subsequent heartbeat sees N=2 → half each.
	resp = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp2"})
	if sh := shareFor(t, resp, "throttle"); sh.RPM != 150 || sh.TPM != 3000 {
		t.Fatalf("two planes must split the limit: %+v", sh)
	}
	resp = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	if sh := shareFor(t, resp, "throttle"); sh.RPM != 150 || sh.TPM != 3000 {
		t.Fatalf("existing plane must shrink to the new split: %+v", sh)
	}

	// A third: 100 each — Σ = limit, never more.
	resp = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp3"})
	if sh := shareFor(t, resp, "throttle"); sh.RPM != 100 || sh.TPM != 2000 {
		t.Fatalf("three planes: %+v", sh)
	}

	// The unlimited rule never produces a share.
	for _, got := range resp.RateShares {
		if got.Rule == "nocap" {
			t.Fatalf("unlimited rate rule must not be shared: %+v", got)
		}
	}
}

func TestRateShareDeadPlaneReleasesItsSlice(t *testing.T) {
	s, ts := newRateShareServer(t)

	doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp2"})
	resp := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	if sh := shareFor(t, resp, "throttle"); sh.RPM != 150 {
		t.Fatalf("two live planes: %+v", sh)
	}

	// dp2 goes quiet: one hour later (beyond the 3×interval liveness
	// horizon, well short of the 24h prune) dp1's next heartbeat reclaims
	// the whole limit.
	base := time.Now()
	s.mu.Lock()
	s.now = func() time.Time { return base.Add(time.Hour) }
	s.mu.Unlock()
	resp = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	if sh := shareFor(t, resp, "throttle"); sh.RPM != 300 || sh.TPM != 6000 {
		t.Fatalf("dead plane's slice must return to the pool within the horizon: %+v", sh)
	}
}

func TestRateShareMinimumOneNeverStarves(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(`apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: tiny }
spec:
  subject: { team: alpha }
  rules:
  - name: throttle
    failurePolicy: FailOpen
    rate: { rpm: 2 }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer("", dir)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	for _, dp := range []string{"dp1", "dp2", "dp3"} {
		doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: dp})
	}
	// limit 2 across 3 planes: floor division would starve a plane at 0;
	// the min-1 floor deliberately wins over exactness (ADR-043).
	resp := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	sh := shareFor(t, resp, "throttle")
	if sh.RPM != 1 {
		t.Fatalf("share must floor at 1, got %+v", sh)
	}
	if sh.TPM != 0 {
		t.Fatalf("undeclared tpm dimension must stay 0 (unclamped): %+v", sh)
	}
}
