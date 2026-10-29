package adminui

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestAuditViewWired(t *testing.T) {
	_, html := get(t, "/index.html")
	_, js := get(t, "/audit.js")
	_, app := get(t, "/app.js")
	for _, want := range []string{`data-view="audit"`, `id="view-audit"`, `data-cap="audit_console"`, `<script src="audit.js"></script>`} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %s", want)
		}
	}
	if !strings.Contains(app, `audit: "nav.audit"`) || !strings.Contains(app, "audit: refreshAuditView") {
		t.Error("app.js VIEWS/refreshers missing audit")
	}
	for _, banned := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "localStorage", "sessionStorage", "eval("} {
		if strings.Contains(js, banned) {
			t.Errorf("audit.js must not use %s", banned)
		}
	}
	for _, want := range []string{"/admin/audit/events", "/admin/audit/sessions", "/admin/audit/summary", "/admin/audit/verify", "/admin/bodies/"} {
		if !strings.Contains(js, want) {
			t.Errorf("audit.js does not call %s", want)
		}
	}
}

func TestAuditI18nKeysHaveFourLanguages(t *testing.T) {
	js, _ := os.ReadFile("static/audit.js")
	html, _ := os.ReadFile("static/index.html")
	dict, _ := os.ReadFile("static/i18n.js")
	keys := map[string]bool{"nav.audit": true}
	for _, m := range regexp.MustCompile(`msg\("((?:audit|nav)\.[A-Za-z0-9_.]+)"\)`).FindAllStringSubmatch(string(js), -1) {
		keys[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`data-i18n(?:-placeholder)?="(audit\.[A-Za-z0-9_.]+)"`).FindAllStringSubmatch(string(html), -1) {
		keys[m[1]] = true
	}
	for _, kind := range []string{"ok", "policy_denied", "privacy_blocked", "privacy_masked", "privacy_internal", "upstream_error", "auto_mode_blocked"} {
		keys["audit.kind."+kind] = true
	}
	for _, scope := range []string{"team", "key", "user", "route"} {
		keys["audit.scope."+scope] = true
	}
	for _, wf := range []string{"gateway", "settle", "denied"} {
		keys["audit.wf."+wf] = true
	}
	// Copy changes that must exist in all four languages even if a call site moves.
	for _, k := range []string{"audit.wf.denied", "audit.autoHint", "audit.autoNotObserved", "audit.classNotReported", "audit.verdictAllow", "audit.verdictBlock", "audit.verdictUnknown", "audit.noTiming", "audit.keyAmbiguous"} {
		keys[k] = true
	}
	for k := range keys {
		line := regexp.MustCompile(`(?m)^\s*"` + regexp.QuoteMeta(k) + `": \{.*\},?$`).FindString(string(dict))
		for _, lang := range []string{"en:", "ko:", "zh:", "ja:"} {
			if line == "" || !strings.Contains(line, lang) {
				t.Errorf("i18n key %s missing %s", k, lang)
			}
		}
	}
}

func TestAuditConsoleNodeChecks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("audit console behavior checks require Node (standard library only)")
	}
	out, err := exec.Command(node, "testdata/audit_console.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("audit_console.cjs failed: %v\n%s", err, out)
	}
}
