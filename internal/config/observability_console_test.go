package config

import (
	"strings"
	"testing"
)

const consoleExporters = `"exporters":{"corp":{"endpoint":"h:1","signals":["traces","audit"]}}`

func TestObservabilityConsoleParses(t *testing.T) {
	cfg, err := loadObservability(t, `{"policies":["/srv/policies","/srv/console/console-observability.yaml"],
		"observability":{`+consoleExporters+`,"console":{"state_file":"/srv/state/observability-limits.json",
		"policy_file":"/srv/console/console-observability.yaml","content_rules":true}}}`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := cfg.Observability.Console
	if c == nil || c.StateFile != "/srv/state/observability-limits.json" || c.PolicyFile != "/srv/console/console-observability.yaml" || !c.ContentRules {
		t.Fatalf("console = %+v", c)
	}
}

func TestObservabilityConsoleRejects(t *testing.T) {
	for _, tc := range []struct{ name, top, console, want string }{
		{"relative state file", ``, `{"state_file":"limits.json"}`, "state_file"},
		{"state file not json", ``, `{"state_file":"/srv/limits.yaml"}`, "state_file"},
		{"state file not clean", ``, `{"state_file":"/srv/../srv/limits.json"}`, "state_file"},
		{"state file listed in policies", `"policies":["/srv/limits.json"],`, `{"state_file":"/srv/limits.json"}`, "policies"},
		{"state file in a policies directory", `"policies":["/srv/policies"],`, `{"state_file":"/srv/policies/limits.json"}`, "policies"},
		{"policy file not yaml", `"policies":["/srv/console.json"],`, `{"policy_file":"/srv/console.json"}`, "policy_file"},
		{"policy file not listed", `"policies":["/srv/policies"],`, `{"policy_file":"/srv/console.yaml"}`, "listed in policies"},
		{"policy file directory also listed", `"policies":["/srv/console","/srv/console/console.yaml"],`, `{"policy_file":"/srv/console/console.yaml"}`, "twice"},
		{"policy file with control plane", `"control_plane":{"url":"https://cp.internal","dataplane":"dp"},`, `{"policy_file":"/srv/console.yaml"}`, "control plane"},
		{"content rules without policy file", ``, `{"content_rules":true}`, "content_rules"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadObservability(t, `{`+tc.top+`"observability":{`+consoleExporters+`,"console":`+tc.console+`}}`)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
