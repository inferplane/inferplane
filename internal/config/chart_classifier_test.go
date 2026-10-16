package config

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const testDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// chartPod renders the chart and returns the Deployment's containers and
// volumes and the rendered gateway config. It fails on any non-data/admin
// Service port.
func chartPod(t *testing.T, args ...string) ([]map[string]any, []any, *Config) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm unavailable; operator chart validation requires helm")
	}
	out, err := exec.Command(helm, append([]string{"template", "test", "../../charts/inferplane"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("chart failed: %s", out)
	}
	var containers []map[string]any
	var volumes []any
	var cfg *Config
	for _, doc := range strings.Split(string(out), "\n---") {
		var object map[string]any
		if err := yaml.Unmarshal([]byte(doc), &object); err != nil {
			t.Fatal(err)
		}
		switch object["kind"] {
		case "ConfigMap":
			data, _ := object["data"].(map[string]any)
			if raw, ok := data["config.json"].(string); ok {
				cfg = &Config{}
				if err := json.Unmarshal([]byte(raw), cfg); err != nil {
					t.Fatal(err)
				}
			}
		case "Deployment":
			spec, _ := object["spec"].(map[string]any)
			template, _ := spec["template"].(map[string]any)
			pod, _ := template["spec"].(map[string]any)
			for _, c := range pod["containers"].([]any) {
				containers = append(containers, c.(map[string]any))
			}
			volumes, _ = pod["volumes"].([]any)
		case "Service":
			spec, _ := object["spec"].(map[string]any)
			for _, p := range spec["ports"].([]any) {
				if name := p.(map[string]any)["name"]; name != "data" && name != "admin" {
					t.Fatalf("unexpected Service port %v", name)
				}
			}
		}
	}
	return containers, volumes, cfg
}

func envValues(container map[string]any) map[string]string {
	out := map[string]string{}
	for _, e := range container["env"].([]any) {
		m := e.(map[string]any)
		if v, ok := m["value"].(string); ok {
			out[m["name"].(string)] = v
		}
	}
	return out
}

func TestChartLayaSidecarIsOptInAndPodLocal(t *testing.T) {
	if off, _, _ := chartPod(t); len(off) != 1 {
		t.Fatalf("the default chart renders %d containers", len(off))
	}
	helm, _ := exec.LookPath("helm")
	if out, err := exec.Command(helm, "template", "test", "../../charts/inferplane", "--set", "piiClassifier.laya.enabled=true").CombinedOutput(); err == nil || !strings.Contains(string(out), "digest is required") {
		t.Fatalf("an undigested sidecar rendered: %s", out)
	}
	on, volumes, _ := chartPod(t, "--set", "piiClassifier.laya.enabled=true", "--set", "piiClassifier.laya.image.digest="+testDigest)
	if len(on) != 2 || on[0]["name"] != "inferplane" || on[1]["name"] != "laya-ko-pii" {
		t.Fatalf("containers %v", on)
	}
	side := on[1]
	if !strings.HasSuffix(side["image"].(string), "@"+testDigest) {
		t.Fatalf("image %v is not digest-pinned", side["image"])
	}
	if _, exposed := side["ports"]; exposed {
		t.Fatal("the sidecar declares a containerPort")
	}
	if env := envValues(side); env["LAYA_HOST"] != "127.0.0.1" || env["LAYA_PORT"] != "8095" {
		t.Fatalf("env %v", env)
	}
	sc := side["securityContext"].(map[string]any)
	if sc["runAsNonRoot"] != true || sc["readOnlyRootFilesystem"] != true || sc["allowPrivilegeEscalation"] != false {
		t.Fatalf("securityContext %v", sc)
	}
	if limits, _ := side["resources"].(map[string]any)["limits"].(map[string]any); limits["cpu"] != nil {
		t.Fatal("a CPU limit throttles classification latency")
	}
	found := false
	for _, v := range volumes {
		found = found || v.(map[string]any)["name"] == "laya-tmp"
	}
	if !found {
		t.Fatal("no writable /tmp for the read-only sidecar")
	}
}

func TestChartPIIClassifierExampleValuesAgree(t *testing.T) {
	on, _, cfg := chartPod(t, "-f", "../../examples/helm.pii-classifier-ko.yaml", "--set", "piiClassifier.laya.image.digest="+testDigest)
	if len(on) != 2 || cfg == nil || cfg.PIIClassification == nil || len(cfg.PIIClassification.Classifiers) != 1 {
		t.Fatalf("example render: %d containers, cfg %+v", len(on), cfg)
	}
	c := cfg.PIIClassification.Classifiers[0]
	if c.Endpoint != "http://127.0.0.1:"+envValues(on[1])["LAYA_PORT"] || c.Mode != "Shadow" || c.DataBoundary != "internal" {
		t.Fatalf("example classifier %+v does not point at the sidecar", c)
	}
}
