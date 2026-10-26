package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func renderOperationalChart(t *testing.T, args ...string) (map[string]any, string) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm unavailable")
	}
	out, err := exec.Command(helm, append([]string{"template", "test", "../../charts/inferplane"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm: %v\n%s", err, out)
	}
	var workload map[string]any
	var configJSON string
	for _, doc := range strings.Split(string(out), "\n---") {
		var object map[string]any
		if err := yaml.Unmarshal([]byte(doc), &object); err != nil {
			t.Fatal(err)
		}
		switch object["kind"] {
		case "Deployment", "StatefulSet":
			workload = object
		case "ConfigMap":
			if data, ok := object["data"].(map[string]any); ok {
				if raw, ok := data["config.json"].(string); ok {
					configJSON = raw
				}
			}
		}
	}
	if workload == nil || configJSON == "" {
		t.Fatal("chart omitted workload or configuration")
	}
	return workload, configJSON
}

func TestChartDefaultAdminSecretBecomesConfiguredToken(t *testing.T) {
	_, raw := renderOperationalChart(t)
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "operator-admin-token")
	cfg, err := loadReviewConfig(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Server.AdminAuth.Tokens) != 1 || cfg.Server.AdminAuth.Tokens[0] != "operator-admin-token" {
		t.Fatal("the documented Secret token does not enable admin authentication")
	}
}

func TestChartSupportsPrivateStorageAndSlowReplay(t *testing.T) {
	for _, args := range [][]string{
		{"--set", "persistence.enabled=true"},
		{"-f", "../../examples/helm.shared-governance.yaml"},
	} {
		workload, _ := renderOperationalChart(t, args...)
		pod := workload["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		sc, _ := pod["securityContext"].(map[string]any)
		if sc["fsGroup"] != float64(65532) {
			t.Error("fresh PVC lacks the nonroot gateway's writable group")
		}
		gateway := pod["containers"].([]any)[0].(map[string]any)
		startup, _ := gateway["startupProbe"].(map[string]any)
		if startup == nil {
			t.Error("liveness can interrupt audit replay before the gateway starts listening")
			continue
		}
		httpGet := startup["httpGet"].(map[string]any)
		if httpGet["path"] != "/healthz" || httpGet["port"] != "admin" {
			t.Errorf("startup must test process health independently of policy readiness: %v", startup)
		}
		if startup["failureThreshold"].(float64)*startup["periodSeconds"].(float64) < 300 {
			t.Error("startup allowance is too short for documented audit replay")
		}
	}
}

func TestChartConfigChangeRollsPodsButPolicyWatchDoesNot(t *testing.T) {
	checksum := func(args ...string) string {
		t.Helper()
		workload, _ := renderOperationalChart(t, args...)
		meta := workload["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)
		annotations, _ := meta["annotations"].(map[string]any)
		value, _ := annotations["checksum/config"].(string)
		if value == "" {
			t.Fatal("restart-required ConfigMap changes do not roll pods")
		}
		return value
	}
	before := checksum()
	if after := checksum("--set", "config.teams.demo.budget.usd_per_day=1"); before == after {
		t.Fatal("team budget config change did not affect pod checksum")
	}
	file := filepath.Join(t.TempDir(), "policy-values.json")
	raw, err := json.Marshal(map[string]any{"policies": map[string]string{"demo.yaml": "apiVersion: inferplane.dev/v1alpha1\nkind: GovernancePolicy\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if after := checksum("-f", file); before != after {
		t.Fatal("watched policy update unnecessarily interrupts inference")
	}
}

func TestChartRejectsMisspelledOrInvalidOperationalValues(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm unavailable")
	}
	for _, args := range [][]string{
		{"--set", "persistnce.enabled=true"},
		{"--set", "replicaCount=1.5"},
		{"--set", "startupProbe.failureThreshold=0"},
		{"--set", "podSecurityContext.fsGroup=-1"},
	} {
		t.Run(args[1], func(t *testing.T) {
			out, err := exec.Command(helm, append([]string{"template", "test", "../../charts/inferplane"}, args...)...).CombinedOutput()
			if err == nil {
				t.Fatalf("invalid operational values silently rendered: %v\n%s", args, out)
			}
		})
	}
}
