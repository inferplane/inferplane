package adminui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Shared console checks: the audit and observability views hold the same
// rendering and i18n rules, so both test files call these helpers.

// assertNoBannedSinks fails when the script uses an HTML-injecting or
// browser-storage sink. Extra sinks are checked on top of the common set.
func assertNoBannedSinks(t *testing.T, file, js string, extra ...string) {
	t.Helper()
	banned := append([]string{"innerHTML", "outerHTML", "insertAdjacentHTML", "localStorage", "sessionStorage", "eval("}, extra...)
	for _, b := range banned {
		if strings.Contains(js, b) {
			t.Errorf("%s must not use %s", file, b)
		}
	}
}

// assertEndpointsListed fails when the script never names one of the admin
// routes the view is supposed to call.
func assertEndpointsListed(t *testing.T, file, js string, endpoints ...string) {
	t.Helper()
	for _, want := range endpoints {
		if !strings.Contains(js, want) {
			t.Errorf("%s does not call %s", file, want)
		}
	}
}

// assertKeysHaveFourLanguages fails when a key has no one-line entry in
// static/i18n.js that carries en, ko, zh and ja.
func assertKeysHaveFourLanguages(t *testing.T, keys map[string]bool) {
	t.Helper()
	dict, err := os.ReadFile("static/i18n.js")
	if err != nil {
		t.Fatal(err)
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
