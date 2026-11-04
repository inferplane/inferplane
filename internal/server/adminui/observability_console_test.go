package adminui

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestObservabilityViewWired(t *testing.T) {
	_, html := get(t, "/index.html")
	_, app := get(t, "/app.js")
	resp, js := get(t, "/observability.js")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("observability.js served as %d %v", resp.StatusCode, resp.Header)
	}
	for _, want := range []string{`data-view="observability"`, `id="view-observability"`, `data-cap="observability_export"`, `<script src="observability.js"></script>`} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %s", want)
		}
	}
	// The audit console mockup places Observability between Audit and Compare models.
	if a, o, e := strings.Index(html, `data-view="audit"`), strings.Index(html, `data-view="observability"`), strings.Index(html, `data-view="evaluation"`); !(a < o && o < e) {
		t.Error("the Observability nav button must sit between Audit and Compare models")
	}
	if !strings.Contains(app, `observability: "nav.observability"`) || !strings.Contains(app, "observability: refreshObservabilityView") {
		t.Error("app.js VIEWS/refreshers missing observability")
	}
	if !strings.Contains(js, "async function refreshObservabilityView(") {
		t.Error("observability.js does not define refreshObservabilityView")
	}
	assertNoBannedSinks(t, "observability.js", js, "document.cookie", "ik_")
	assertEndpointsListed(t, "observability.js", js, `"/admin/observability"`, `"/admin/observability/limits"`, `"/admin/observability/rules"`)
}

func TestObservabilityI18nKeysHaveFourLanguages(t *testing.T) {
	js, err := os.ReadFile("static/observability.js")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := os.ReadFile("static/index.html")
	keys := map[string]bool{"nav.observability": true}
	for _, m := range regexp.MustCompile(`"(obs\.[A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*)"`).FindAllStringSubmatch(string(js), -1) {
		keys[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`data-i18n(?:-placeholder)?="(obs\.[A-Za-z0-9_.]+)"`).FindAllStringSubmatch(string(html), -1) {
		keys[m[1]] = true
	}
	for prefix, names := range map[string][]string{
		"obs.source.":   {"files", "control_plane", "none", "console"},
		"obs.reason.":   {"not_configured", "no_state_file", "no_policy_file", "control_plane", "foreign_documents", "read_failed"},
		"obs.field.":    {"paused", "signals", "max_content_bytes", "max_sample_percent"},
		"obs.action.":   {"added", "changed", "removed"},
		"obs.decision.": {"selected", "no_rule", "content_conflict", "withheld_limit"},
		"obs.err.": {"revision_conflict", "limits_file_changed", "read_only", "policy_reload_failed", "console_file_foreign",
			"content_not_allowed", "audit_unavailable", "invalid_limits", "invalid_rules", "invalid_request", "persist_failed", "read_failed",
			"restore_failed.limits", "restore_failed.rules"},
	} {
		for _, n := range names {
			keys[prefix+n] = true
		}
	}
	// Copy added by controller rulings: not-counted legacy row and data boundary.
	for _, k := range []string{"obs.notCounted", "obs.boundary"} {
		keys[k] = true
	}
	assertKeysHaveFourLanguages(t, keys)
}

func TestObservabilityConsoleNodeChecks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("observability console helper checks require Node (standard library only)")
	}
	out, err := exec.Command(node, "testdata/observability_console.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("observability_console.cjs failed: %v\n%s", err, out)
	}
}
