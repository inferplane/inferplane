package main

// E2E for control-plane mode (ADR-034): a gateway configured with
// control_plane picks up policy over the first heartbeat, enforces
// modelAccess/budget from the distributed documents, and reports itself in
// the control plane's dataplane view.

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

const cpE2EPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: cp-team }
spec:
  subject: { team: cp-team }
  rules:
  - name: test-model-only
    failurePolicy: FailOpen
    modelAccess: { allow: ["claude-test"] }
  - name: cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 1000
      hardCap: true
      lease: { grantMilliUSD: 1000, renewInterval: "1s" }
`

func TestE2EControlPlaneDistributesAndEnforces(t *testing.T) {
	t.Setenv("CP_E2E_TOKEN", "cp-tok")

	// Real control plane on a test listener.
	polDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(cpE2EPolicyYAML), 0o600); err != nil {
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
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		cfg["models"].(map[string]any)["claude-other"] = map[string]any{
			"targets": []any{map[string]any{"provider": "up", "model": "claude-other"}},
		}
		cfg["pricing"].(map[string]any)["overrides"].(map[string]any)["up"].(map[string]any)["claude-other"] = map[string]any{"input_per_mtok": 1.0, "output_per_mtok": 1.0}
		cfg["control_plane"] = map[string]any{
			"url":       cpSrv.URL,
			"token_ref": map[string]any{"env": "CP_E2E_TOKEN"},
			"dataplane": "e2e-dp",
		}
	})

	_, key := createKey(t, adminURL, "cp-team", []string{"*"})

	// The boot heartbeat is asynchronous — poll until the distributed
	// modelAccess rule bites (denied model flips to 403).
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := postMessages(t, dataURL, key, "claude-other")
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode == http.StatusForbidden {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("distributed modelAccess never enforced (last status %d)", r.StatusCode)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The allowed model flows.
	r := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("allowed model: status %d, want 200", r.StatusCode)
	}

	// The data plane registered itself with its API versions.
	req, _ := http.NewRequest(http.MethodGet, cpSrv.URL+"/v1alpha1/dataplanes", nil)
	req.Header.Set("Authorization", "Bearer cp-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var view struct {
		Dataplanes map[string]struct {
			APIVersions []string `json:"apiVersions"`
		} `json:"dataplanes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	dp, ok := view.Dataplanes["e2e-dp"]
	if !ok || len(dp.APIVersions) == 0 {
		t.Fatalf("data plane not registered: %+v", view.Dataplanes)
	}
}
