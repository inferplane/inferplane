package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/policy"
)

const cpPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-a }
spec:
  subject: { team: alpha }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 100          # $0.10 = 100,000 µUSD
      hardCap: true
      lease: { grantMilliUSD: 10, renewInterval: "5s" }   # 10,000 µUSD per grant
  - name: models
    failurePolicy: FailOpen
    modelAccess: { allow: ["claude-haiku-4-5"] }
`

func newTestServer(t *testing.T, token string) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(cpPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(token, dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

func doSync(t *testing.T, url, token string, req policy.SyncRequest) policy.SyncResponse {
	t.Helper()
	body, _ := json.Marshal(&req)
	hreq, _ := http.NewRequest(http.MethodPost, url+"/v1alpha1/sync", bytes.NewReader(body))
	hreq.Header.Set("Content-Type", "application/json")
	if token != "" {
		hreq.Header.Set("Authorization", "Bearer "+token)
	}
	hresp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Fatalf("sync: status %d", hresp.StatusCode)
	}
	var resp policy.SyncResponse
	if err := json.NewDecoder(hresp.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSyncDistributesPoliciesAndLeases(t *testing.T) {
	_, ts := newTestServer(t, "")

	resp := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1", APIVersions: policy.SupportedAPIVersions})
	if len(resp.Policies) != 1 || resp.Policies[0].Metadata.Name != "team-a" {
		t.Fatalf("policies not distributed: %+v", resp.Policies)
	}
	if resp.Generation == "" {
		t.Fatal("no generation")
	}
	if resp.SyncIntervalSeconds != 5 {
		t.Fatalf("interval = %d, want 5 (tightest lease renew)", resp.SyncIntervalSeconds)
	}
	if len(resp.Leases) != 1 {
		t.Fatalf("leases = %+v, want 1", resp.Leases)
	}
	l := resp.Leases[0]
	// Fresh data plane, zero spend: allowance = one grant (10,000 µUSD).
	if l.Team != "alpha" || !l.HardCap || l.AllowanceMicroUSD != 10_000 {
		t.Fatalf("lease mangled: %+v", l)
	}

	// Same generation echoed back → policy payload omitted, leases still flow.
	resp2 := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1", Generation: resp.Generation})
	if resp2.Policies != nil {
		t.Fatal("unchanged generation must omit the policy payload")
	}
	if len(resp2.Leases) != 1 {
		t.Fatal("leases must flow on every heartbeat")
	}
}

// The global-budget invariant: across ANY number of data planes, the sum of
// outstanding allowances beyond reported spend never exceeds limit − Σspent.
func TestLeaseLedgerBoundsGlobalOvershoot(t *testing.T) {
	_, ts := newTestServer(t, "")

	// limit 100,000 µUSD; grant 10,000. dp1 reports 60,000 spent.
	r1 := doSync(t, ts.URL, "", policy.SyncRequest{
		Dataplane: "dp1",
		Reports:   []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 60_000}},
	})
	if got := r1.Leases[0].AllowanceMicroUSD; got != 70_000 {
		t.Fatalf("dp1 allowance = %d, want 70000 (spent+grant)", got)
	}

	// dp2 reports 25,000: remaining = 100k − 85k − dp1 outstanding 10k = 5k
	// → grant clipped to 5k, allowance 30k.
	r2 := doSync(t, ts.URL, "", policy.SyncRequest{
		Dataplane: "dp2",
		Reports:   []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 25_000}},
	})
	if got := r2.Leases[0].AllowanceMicroUSD; got != 30_000 {
		t.Fatalf("dp2 allowance = %d, want 30000 (grant clipped to global remaining)", got)
	}

	// dp3 arrives with zero spend: nothing remains → allowance 0 (its hard
	// cap fails closed at the data plane).
	r3 := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp3"})
	if got := r3.Leases[0].AllowanceMicroUSD; got != 0 {
		t.Fatalf("dp3 allowance = %d, want 0 (global budget exhausted)", got)
	}

	// A DECREASE in dp1's cumulative spend means its budget window rolled
	// over: the ledger adopts the fresh counter and regrants from it — the
	// old 70k allowance must NOT carry into the new window.
	r4 := doSync(t, ts.URL, "", policy.SyncRequest{
		Dataplane: "dp1",
		Reports:   []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 1}},
	})
	if got := r4.Leases[0].AllowanceMicroUSD; got >= 70_000 {
		t.Fatalf("window rollover carried the old allowance: %d", got)
	}
}

func TestDataplanesListsVersionsAndRejections(t *testing.T) {
	_, ts := newTestServer(t, "")
	doSync(t, ts.URL, "", policy.SyncRequest{
		Dataplane:   "dp1",
		APIVersions: []string{"inferplane.dev/v1alpha1"},
		Rejections:  []policy.Rejection{{Policy: "team-a", Rule: "future", Reason: "unknown rule kind"}},
	})

	hresp, err := http.Get(ts.URL + "/v1alpha1/dataplanes")
	if err != nil {
		t.Fatal(err)
	}
	defer hresp.Body.Close()
	var body struct {
		Generation string             `json:"generation"`
		Dataplanes map[string]*dpInfo `json:"dataplanes"`
	}
	if err := json.NewDecoder(hresp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	dp, ok := body.Dataplanes["dp1"]
	if !ok || len(dp.APIVersions) != 1 || len(dp.Rejections) != 1 {
		t.Fatalf("dataplane view mangled: %+v", body.Dataplanes)
	}
}

func TestAuthRequiredWhenTokenSet(t *testing.T) {
	_, ts := newTestServer(t, "s3cret")

	body, _ := json.Marshal(&policy.SyncRequest{Dataplane: "dp1"})
	resp, err := http.Post(ts.URL+"/v1alpha1/sync", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token: status %d, want 401", resp.StatusCode)
	}
	doSync(t, ts.URL, "s3cret", policy.SyncRequest{Dataplane: "dp1"}) // correct token passes
}

// Reload keeps spend for surviving rules and re-fingerprints the set.
func TestReloadPreservesLedger(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(f, []byte(cpPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer("", dir)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	doSync(t, ts.URL, "", policy.SyncRequest{
		Dataplane: "dp1",
		Reports:   []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 90_000}},
	})
	gen1 := s.generation

	// Widen the model list (same budget rule survives).
	edited := bytes.Replace([]byte(cpPolicyYAML), []byte(`allow: ["claude-haiku-4-5"]`), []byte(`allow: ["*"]`), 1)
	if err := os.WriteFile(f, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if s.generation == gen1 {
		t.Fatal("generation did not change after edit")
	}
	r := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	// Spend survived: allowance = 90k + min(grant 10k, remaining 10k) = 100k.
	if got := r.Leases[0].AllowanceMicroUSD; got != 100_000 {
		t.Fatalf("allowance after reload = %d, want 100000 (ledger must survive)", got)
	}
}

// Review finding (major): a data plane whose lease horizon passed must have
// its unspent allowance RELEASED — mayu's default instance id is per-boot,
// so restart churn would otherwise permanently strand outstanding grants and
// starve every live proxy's remaining budget. And after pruneAfter, the dead
// data plane's ledger rows disappear entirely.
func TestExpiredLeaseAllowanceReleasedAndPruned(t *testing.T) {
	dir := t.TempDir()
	// Tight limit so outstanding grants visibly clip: limit 15k µUSD,
	// grant 10k, renew 5s (lease horizon 15s).
	tight := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-a }
spec:
  subject: { team: alpha }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 15
      hardCap: true
      lease: { grantMilliUSD: 10, renewInterval: "5s" }
`
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(tight), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer("", dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	s.now = func() time.Time { return clock }
	mux := http.NewServeMux()
	s.Mount(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// dp1 takes a 10k grant; dp2 is clipped to the remaining 5k.
	doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	r := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp2"})
	if got := r.Leases[0].AllowanceMicroUSD; got != 5_000 {
		t.Fatalf("dp2 allowance while dp1 live = %d, want 5000", got)
	}

	// dp1 goes silent past its 15s lease horizon: its unspent 10k is
	// released, so dp2's next heartbeat gets the full grant.
	clock = clock.Add(16 * time.Second)
	r = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp2"})
	if got := r.Leases[0].AllowanceMicroUSD; got != 10_000 {
		t.Fatalf("dp2 allowance after dp1 lease expiry = %d, want 10000 (stranded grant must be released)", got)
	}

	// dp1 had actually spent 3k before dying: that spend still counts...
	s.mu.Lock()
	s.ledger[ruleKey{policy: "team-a", rule: "cap"}].spent["dp1"] = 3_000
	s.mu.Unlock()
	r = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp2"})
	if got := r.Leases[0].AllowanceMicroUSD; got != 10_000 {
		// remaining = 15k − 3k(dp1) − 0 = 12k → grant uncapped at 10k.
		t.Fatalf("dp2 allowance with dead dp1 spend = %d, want 10000", got)
	}

	// ...until pruneAfter passes and dp1's rows are dropped wholesale.
	clock = clock.Add(pruneAfter + time.Minute)
	doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp2"})
	s.mu.Lock()
	_, spentKept := s.ledger[ruleKey{policy: "team-a", rule: "cap"}].spent["dp1"]
	_, dpKept := s.dataplanes["dp1"]
	s.mu.Unlock()
	if spentKept || dpKept {
		t.Fatal("dp1 rows must be pruned after pruneAfter")
	}
}

// PR #50 review finding (data race): the dataplane view must deep-copy under
// the lock — encoding shared *dpInfo pointers after unlock races with
// concurrent heartbeats. Run with -race.
func TestDataplanesViewConcurrentWithSync(t *testing.T) {
	_, ts := newTestServer(t, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 30; i++ {
			doSync(t, ts.URL, "", policy.SyncRequest{
				Dataplane:   "dp-race",
				APIVersions: []string{"inferplane.dev/v1alpha1"},
				Rejections:  []policy.Rejection{{Policy: "p", Reason: "r"}},
				Reports:     []policy.ConsumptionReport{{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: int64(i)}},
			})
		}
	}()
	for i := 0; i < 30; i++ {
		resp, err := http.Get(ts.URL + "/v1alpha1/dataplanes")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	<-done
}

// PR #50 review: the grant math saturates instead of wrapping — a wrapped
// sum would report a spuriously LOW global total and mint allowance the
// budget does not have.
func TestSatAddSaturates(t *testing.T) {
	if got := satAdd(math.MaxInt64-1, 5); got != math.MaxInt64 {
		t.Fatalf("satAdd near ceiling = %d, want MaxInt64", got)
	}
	if got := satAdd(3, 4); got != 7 {
		t.Fatalf("satAdd(3,4) = %d, want 7", got)
	}
	if got := satAdd(0, math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("satAdd(0,Max) = %d, want MaxInt64", got)
	}
}
