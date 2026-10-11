package config

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The default chart ships audit records on stdout for a node collector and
// keeps nothing that grows on the pod volume: no file sink, no local analytics
// index. A regression here reintroduces unbounded sidecar disk growth.
func TestDefaultChartAuditsToStdoutWithoutLocalGrowth(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm unavailable; operator chart validation requires helm")
	}
	out, err := exec.Command(helm, "template", "test", "../../charts/inferplane").CombinedOutput()
	if err != nil {
		t.Fatalf("default chart failed: %s", out)
	}
	var cfg *Config
	var resources map[string]any
	for _, doc := range strings.Split(string(out), "\n---") {
		var object map[string]any
		if err := yaml.Unmarshal([]byte(doc), &object); err != nil {
			t.Fatal(err)
		}
		switch object["kind"] {
		case "ConfigMap":
			data, _ := object["data"].(map[string]any)
			raw, ok := data["config.json"].(string)
			if !ok {
				continue
			}
			cfg = &Config{}
			if err := json.Unmarshal([]byte(raw), cfg); err != nil {
				t.Fatal(err)
			}
		case "Deployment":
			spec, _ := object["spec"].(map[string]any)
			template, _ := spec["template"].(map[string]any)
			pod, _ := template["spec"].(map[string]any)
			containers, _ := pod["containers"].([]any)
			if len(containers) == 0 {
				t.Fatal("chart deployment has no spec.template.spec.containers")
			}
			container, _ := containers[0].(map[string]any)
			resources, _ = container["resources"].(map[string]any)
			if len(resources) == 0 {
				t.Fatal("chart deployment's first container has missing, empty or invalid resources")
			}
		}
	}
	if cfg == nil || resources == nil {
		t.Fatal("chart rendered no config or deployment")
	}
	if len(cfg.Audit.Sinks) != 1 || cfg.Audit.Sinks[0].Type != "stdout" {
		t.Fatalf("audit sinks = %+v, want exactly one stdout sink", cfg.Audit.Sinks)
	}
	if cfg.Audit.Buffer.Path == "" || cfg.Audit.FailureMode != "buffer_then_block" {
		t.Fatalf("audit WAL/failure mode missing: %+v", cfg.Audit)
	}
	if _, on := ResolveAnalytics(cfg); on {
		t.Fatal("default chart enables the unbounded local analytics index")
	}
	limits, _ := resources["limits"].(map[string]any)
	requests, _ := resources["requests"].(map[string]any)
	if requests["cpu"] == nil || requests["memory"] == nil || limits["memory"] == nil {
		t.Fatalf("resources = %v, want cpu/memory requests and a memory limit", resources)
	}
	if limits["cpu"] != nil {
		t.Fatal("a CPU limit throttles in-flight streams; leave it unset")
	}
}
