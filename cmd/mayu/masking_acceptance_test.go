package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewayOptionalMaskingAdminPersistence(t *testing.T) {
	t.Setenv("OBS_CONSOLE_ADMIN", consoleAdmin)
	var dir string
	path := routingAcceptanceConfig(t, nil, func(cfg map[string]any, d string) {
		dir = d
		delete(cfg, "control_plane")
		withConsoleAdmin(cfg)
		cfg["masking"] = map[string]any{"state_file": filepath.Join(d, "masking.json")}
	})
	g, _ := bootObservability(t, path)
	// Exercise the actual full-admin route registration, not just its handler.
	rec := httptest.NewRecorder()
	g.adminSrv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/masking", nil))
	if rec.Code != 401 {
		t.Fatalf("unauthenticated settings: %d", rec.Code)
	}
	code, before := consoleCall(t, g, "GET", "/admin/masking", nil)
	if code != 200 || before["enabled"] != false || before["writable"] != true {
		t.Fatalf("default settings: %d %v", code, before)
	}
	code, after := consoleCall(t, g, "PUT", "/admin/masking", map[string]any{
		"enabled": true, "expected_revision": before["revision"],
	})
	if code != 200 || after["enabled"] != true {
		t.Fatalf("enable: %d %v", code, after)
	}
	flushObservability(t, g)
	raw, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil || !strings.Contains(string(raw), `"event":"admin_masking_changed"`) ||
		!strings.Contains(string(raw), `"after":"`+after["revision"].(string)+`"`) {
		t.Fatalf("missing audited state change: %v", err)
	}
	g2, _ := bootObservability(t, path)
	defer flushObservability(t, g2)
	code, restarted := consoleCall(t, g2, "GET", "/admin/masking", nil)
	if code != 200 || restarted["enabled"] != true || restarted["revision"] != after["revision"] {
		t.Fatalf("restart lost choice: %d %v", code, restarted)
	}
	code, disabled := consoleCall(t, g2, "PUT", "/admin/masking", map[string]any{
		"enabled": false, "expected_revision": restarted["revision"],
	})
	if code != 200 || disabled["enabled"] != false {
		t.Fatalf("disable: %d %v", code, disabled)
	}
}
