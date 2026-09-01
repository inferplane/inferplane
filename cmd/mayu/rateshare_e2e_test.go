package main

// ADR-043 e2e (roadmap ①'s acceptance shape): two gateways against one
// control plane admit at most the GLOBAL rate limit in aggregate — the 429
// appears at the configured rpm, not N× it.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/controlplane"
)

const rateShareE2EPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: cp-team }
spec:
  subject: { team: cp-team }
  rules:
  - name: test-model-only
    failurePolicy: FailOpen
    modelAccess: { allow: ["claude-test"] }
  - name: throttle
    failurePolicy: FailOpen
    rate: { rpm: 4 }
  - name: cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 100000
      hardCap: true
      lease: { grantMilliUSD: 1000, renewInterval: "1s" }
`

func TestE2ERateSharesBoundFleetAggregate(t *testing.T) {
	t.Setenv("CP_RS_TOKEN", "cp-tok")

	polDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(rateShareE2EPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer("cp-tok", polDir)
	if err != nil {
		t.Fatal(err)
	}
	cpMux := http.NewServeMux()
	cp.Mount(cpMux)
	cpSrv := httptest.NewServer(cpMux)
	defer cpSrv.Close()

	up := newAnthropicUpstream(t)
	bootOne := func(dpID string) (dataURL, adminURL string) {
		dataURL, adminURL, _ = bootGateway(t, func(cfg map[string]any, dir string) {
			teamsAPIConfig(up.srv.URL)(cfg, dir)
			cfg["models"].(map[string]any)["claude-other"] = map[string]any{
				"targets": []any{map[string]any{"provider": "up", "model": "claude-other"}},
			}
			// Cheap rates: this test is about RATE, and the budget rule
			// exists only to force a 1s heartbeat — spend must stay far
			// below the cap so no 402 masks the 429 behavior. (teamsAPIConfig
			// prices claude-test at $1M/mtok for the budget e2e tests.)
			cfg["pricing"].(map[string]any)["overrides"].(map[string]any)["up"].(map[string]any)["claude-test"] = map[string]any{"input_per_mtok": 1.0, "output_per_mtok": 1.0}
			cfg["pricing"].(map[string]any)["overrides"].(map[string]any)["up"].(map[string]any)["claude-other"] = map[string]any{"input_per_mtok": 1.0, "output_per_mtok": 1.0}
			cfg["control_plane"] = map[string]any{
				"url":       cpSrv.URL,
				"token_ref": map[string]any{"env": "CP_RS_TOKEN"},
				"dataplane": dpID,
			}
		})
		return dataURL, adminURL
	}
	data1, admin1 := bootOne("rs-dp1")
	data2, admin2 := bootOne("rs-dp2")
	_, key1 := createKey(t, admin1, "cp-team", []string{"*"})
	_, key2 := createKey(t, admin2, "cp-team", []string{"*"})

	// Wait until BOTH planes have applied the distributed policy, probing
	// with the DENIED model only — a 403 is rejected at RBAC, before
	// governance, so these probes charge no rate tokens.
	for _, probe := range []struct{ url, key string }{{data1, key1}, {data2, key2}} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			r := postMessages(t, probe.url, probe.key, "claude-other")
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			if r.StatusCode == http.StatusForbidden {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("distributed policy never enforced on %s (last status %d)", probe.url, r.StatusCode)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Both planes are registered; give each one more heartbeat (1s cadence
	// from the lease renewInterval) so both hold the settled N=2 share
	// (rpm 4 / 2 planes = 2 each).
	time.Sleep(2500 * time.Millisecond)

	// Burst 6 allowed requests at EACH plane. Per-plane in-memory buckets
	// without shares would admit 4 each (8 total — the N× failure). With
	// shares each plane's bucket holds its 2-token slice.
	okPerPlane := map[string]int{}
	for _, plane := range []struct {
		name, url, key string
	}{{"dp1", data1, key1}, {"dp2", data2, key2}} {
		for i := 0; i < 6; i++ {
			r := postMessages(t, plane.url, plane.key, "claude-test")
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			switch r.StatusCode {
			case http.StatusOK:
				okPerPlane[plane.name]++
			case http.StatusTooManyRequests:
				// the expected rejection
			default:
				t.Fatalf("%s: unexpected status %d", plane.name, r.StatusCode)
			}
		}
	}

	// The remote debug snapshot (roadmap ④) shows the same state doctor
	// would: the team, its 2-rpm share, and a lease.
	dreq, _ := http.NewRequest(http.MethodGet, admin1+"/admin/debug/governance", nil)
	dreq.Header.Set("Authorization", "Bearer "+e2eAdminToken)
	dresp, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		PolicySource string `json:"policy_source"`
		Teams        map[string]struct {
			Share  *struct{ RPM int64 }      `json:"share"`
			Leases []struct{ Period string } `json:"leases"`
		} `json:"teams"`
	}
	if err := json.NewDecoder(dresp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	dresp.Body.Close()
	team, ok := snap.Teams["cp-team"]
	if snap.PolicySource != "control_plane" || !ok || team.Share == nil || team.Share.RPM != 2 || len(team.Leases) == 0 {
		t.Fatalf("debug snapshot missing live governance state: %+v", snap)
	}

	total := okPerPlane["dp1"] + okPerPlane["dp2"]
	if total > 4 {
		t.Fatalf("fleet admitted %d requests against a global rpm of 4 (per plane: %+v) — shares not enforced", total, okPerPlane)
	}
	for name, ok := range okPerPlane {
		if ok == 0 {
			t.Fatalf("%s admitted nothing — a share must never starve a plane (per plane: %+v)", name, okPerPlane)
		}
		if ok > 2 {
			t.Fatalf("%s admitted %d > its 2-rpm share (per plane: %+v)", name, ok, okPerPlane)
		}
	}
}
