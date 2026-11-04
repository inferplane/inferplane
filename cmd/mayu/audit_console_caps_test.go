package main

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
)

func auditCapsFor(t *testing.T, mutate func(cfg map[string]any)) map[string]any {
	t.Helper()
	t.Setenv("E2E_AUDIT_CAPS_TOKEN", "caps-admin-token")
	cfgPath := writeTestConfig(t, func(cfg map[string]any, dir string) {
		cfg["server"].(map[string]any)["admin_auth"] = map[string]any{
			"token_refs": []any{map[string]any{"env": "E2E_AUDIT_CAPS_TOKEN"}},
		}
		if mutate != nil {
			mutate(cfg)
		}
	})
	g, err := newGateway(cfgPath)
	if err != nil {
		t.Fatalf("newGateway: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = g.serve(ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })
	resp := getAdmin(t, "http://"+g.AdminAddr()+"/admin/capabilities", "caps-admin-token")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var caps map[string]any
	if err := json.Unmarshal(body, &caps); err != nil {
		t.Fatalf("capabilities body %q: %v", body, err)
	}
	return caps
}

func TestCapabilitiesAuditConsole(t *testing.T) {
	// Mode A (default: file sink -> local analytics index) advertises the console.
	caps := auditCapsFor(t, nil)
	if caps["analytics_index"] != "A" || caps["audit_console"] != true {
		t.Fatalf("mode A caps = %v", caps)
	}
	if _, ok := caps["trace_link_template"]; ok {
		t.Fatalf("trace_link_template must be omitted when unset: %v", caps)
	}
	// Without an analytics index the console is off.
	caps = auditCapsFor(t, func(cfg map[string]any) { cfg["analytics"] = map[string]any{"disabled": true} })
	if caps["audit_console"] != false {
		t.Fatalf("analytics disabled caps = %v", caps)
	}
	// otel.trace_link_template is advertised verbatim.
	const tpl = "https://grafana.example/explore?traceId={trace_id}"
	caps = auditCapsFor(t, func(cfg map[string]any) {
		cfg["otel"] = map[string]any{"endpoint": "127.0.0.1:4318", "trace_link_template": tpl}
	})
	if caps["trace_link_template"] != tpl {
		t.Fatalf("trace link caps = %v", caps)
	}
}
