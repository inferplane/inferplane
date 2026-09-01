package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/controlplane"
)

// doctorConfig writes a minimal valid gateway config and returns its path.
// controlPlaneURL == "" leaves the gateway standalone.
func doctorConfig(t *testing.T, controlPlaneURL, pricingBlock string) string {
	t.Helper()
	dir := t.TempDir()
	cp := ""
	if controlPlaneURL != "" {
		cp = fmt.Sprintf(`"control_plane": {"url": %q, "token_ref": {"env": "DOCTOR_CP_TOKEN"}},`, controlPlaneURL)
	}
	cfg := fmt.Sprintf(`{
  "server": {
    "listen": "127.0.0.1:0",
    "admin_listen": "127.0.0.1:0",
    "admin_auth": {"token_refs": [{"env": "INFERPLANE_ADMIN_TOKEN"}]}
  },
  "key_store": {"type": "sqlite", "path": %q},
  "audit": {"failure_mode": "buffer_then_block", "buffer": {"path": %q}, "sinks": [{"type": "file", "path": %q}]},
  %s
  "providers": {"ant": {"type": "anthropic", "base_url": "https://api.anthropic.com", "api_key_ref": {"env": "DOCTOR_UPSTREAM_KEY"}}},
  "models": {"m1": {"targets": [{"provider": "ant", "model": "m1"}]}},
  %s
}`, filepath.Join(dir, "keys.db"), filepath.Join(dir, "audit.wal"), filepath.Join(dir, "audit.jsonl"), cp, pricingBlock)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const pricedBlock = `"pricing": {"on_missing": "block", "version": "t", "overrides": {"ant": {"m1": {"input_per_mtok": 3.0, "output_per_mtok": 15.0}}}}`

func levelOf(rep *doctorReport, name string) []string {
	var out []string
	for _, c := range rep.Checks {
		if c.Name == name {
			out = append(out, c.Level)
		}
	}
	return out
}

func TestDoctorHealthyStandalone(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "x")
	t.Setenv("DOCTOR_UPSTREAM_KEY", "sk-test")
	rep := runDoctor(doctorConfig(t, "", pricedBlock), true)
	for _, c := range rep.Checks {
		if c.Level == "fail" {
			t.Fatalf("healthy config produced a failing check: %+v", c)
		}
	}
}

func TestDoctorUnparseableConfigFailsFast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := runDoctor(path, true)
	if got := levelOf(rep, "config"); len(got) != 1 || got[0] != "fail" {
		t.Fatalf("config check = %v, want [fail]", got)
	}
	if len(rep.Checks) != 1 {
		t.Fatalf("an unparseable config must short-circuit, got %d checks", len(rep.Checks))
	}
}

func TestDoctorMissingSecretRefIsItsOwnDiagnosis(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "x")
	os.Unsetenv("DOCTOR_UPSTREAM_KEY") // the provider's key ref must not resolve
	rep := runDoctor(doctorConfig(t, "", pricedBlock), true)
	if got := levelOf(rep, "config"); len(got) != 1 || got[0] != "ok" {
		t.Fatalf("config parses, so config check = %v, want [ok]", got)
	}
	if got := levelOf(rep, "secrets"); len(got) != 1 || got[0] != "fail" {
		t.Fatalf("secrets check = %v, want [fail]", got)
	}
	for _, c := range rep.Checks {
		if c.Name == "secrets" && c.Level == "fail" {
			// The detail must name the ref, never carry a resolved value.
			if want := "DOCTOR_UPSTREAM_KEY"; !strings.Contains(c.Detail, want) {
				t.Fatalf("secrets failure should name the unresolvable ref %q: %s", want, c.Detail)
			}
		}
	}
}

func TestDoctorUnpricedRouteFails(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "x")
	t.Setenv("DOCTOR_UPSTREAM_KEY", "sk-test")
	rep := runDoctor(doctorConfig(t, "", `"pricing": {"on_missing": "allow", "version": "t"}`), true)
	if got := levelOf(rep, "pricing"); len(got) != 1 || got[0] != "fail" {
		t.Fatalf("pricing check = %v, want [fail] (route without a rate)", got)
	}
}

func TestDoctorControlPlaneAuth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(`apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: t }
spec:
  subject: { team: alpha }
  rules:
  - name: models
    failurePolicy: FailOpen
    modelAccess: { allow: ["m1"] }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer("right-token", dir)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	cp.Mount(mux)
	// The doctor hits /readyz for reachability; inferplaned mounts it in
	// main, so mirror it here the same unauthenticated way.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"apiVersions":["inferplane.dev/v1alpha1"]}`)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	t.Setenv("INFERPLANE_ADMIN_TOKEN", "x")
	t.Setenv("DOCTOR_UPSTREAM_KEY", "sk-test")

	t.Setenv("DOCTOR_CP_TOKEN", "right-token")
	rep := runDoctor(doctorConfig(t, ts.URL, pricedBlock), true)
	for _, lvl := range levelOf(rep, "control-plane") {
		if lvl == "fail" {
			t.Fatalf("reachable control plane with the right token must not fail: %+v", rep.Checks)
		}
	}

	t.Setenv("DOCTOR_CP_TOKEN", "wrong-token")
	rep = runDoctor(doctorConfig(t, ts.URL, pricedBlock), true)
	failed := false
	for _, lvl := range levelOf(rep, "control-plane") {
		if lvl == "fail" {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("wrong token must produce a failing control-plane check: %+v", rep.Checks)
	}
}
