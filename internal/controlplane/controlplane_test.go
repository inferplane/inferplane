package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
