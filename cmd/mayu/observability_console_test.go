package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/server"
)

const consoleAdmin = "console-admin-token"

// withConsoleAdmin adds a break-glass admin token; the test sets
// OBS_CONSOLE_ADMIN to consoleAdmin.
func withConsoleAdmin(cfg map[string]any) {
	cfg["server"].(map[string]any)["admin_auth"] = map[string]any{"token_refs": []any{map[string]any{"env": "OBS_CONSOLE_ADMIN"}}}
}

func consoleCall(t *testing.T, g *gateway, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+consoleAdmin)
	rec := httptest.NewRecorder()
	g.adminSrv.Handler.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: status %d, body %q", method, path, rec.Code, rec.Body)
	}
	return rec.Code, out
}

// Review Focus 2: a pause applied from the console is persisted, enforced on
// the request path, audited, and still in force after a restart.
func TestGatewayConsoleLimitSurvivesRestart(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	t.Setenv("OBS_CONSOLE_ADMIN", consoleAdmin)
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	doc := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})
	var dir string
	path := routingAcceptanceConfig(t, &doc, func(cfg map[string]any, d string) {
		dir = d
		withExporters(corp, vault)(cfg, d)
		cfg["observability"].(map[string]any)["console"] = map[string]any{"state_file": filepath.Join(d, "observability-limits.json")}
		withConsoleAdmin(cfg)
	})
	g, key := bootObservability(t, path)
	code, st := consoleCall(t, g, "GET", "/admin/observability", nil)
	limits := st["limits"].(map[string]any)
	if code != 200 || limits["editable"] != true {
		t.Fatalf("status %d, limits %v", code, limits)
	}
	body := map[string]any{"dry_run": true, "expected_revision": limits["revision"], "exporters": map[string]any{"corp": map[string]any{"paused": true}}}
	if code, res := consoleCall(t, g, "PUT", "/admin/observability/limits", body); code != 200 || res["dry_run"] != true {
		t.Fatalf("dry run %d %v", code, res)
	}
	state := filepath.Join(dir, "observability-limits.json")
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the state file: %v", err)
	}
	body["dry_run"] = false
	code, applied := consoleCall(t, g, "PUT", "/admin/observability/limits", body)
	if code != 200 {
		t.Fatalf("apply %d %v", code, applied)
	}
	if rec := routingCall(t, g, key, "after the pause"); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	flushObservability(t, g)
	if n := len(corp.events("inferplane.audit")); n != 0 {
		t.Fatalf("a paused exporter received %d audit copies", n)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"event":"admin_observability_changed"`) || !strings.Contains(string(raw), `"after":"`+applied["result_revision"].(string)+`"`) {
		t.Fatal("the apply left no admin_observability_changed record naming the new revision")
	}
	// The change record's actor is what principal.AdminAuditRef gives the
	// break-glass admin under the gateway's identity configuration (unconfigured
	// here: the legacy user + auth_method encoding, never a digest).
	idCfg, err := principal.IdentityConfig(context.Background(), g.store)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(principal.AdminAuditRef(principal.AdminIdentity{Subject: "break-glass", AuthMethod: "break_glass"}, idCfg.Organization))
	if err != nil {
		t.Fatal(err)
	}
	var changed *audit.Record
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec audit.Record
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Event == "admin_observability_changed" {
			changed = &rec
		}
	}
	if changed == nil {
		t.Fatal("no parsable admin_observability_changed record")
	}
	if got, _ := json.Marshal(changed.Principal); !bytes.Equal(got, want) {
		t.Fatalf("change actor = %s, want %s", got, want)
	}

	g2, err := newGateway(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		g2.dataLn.Close()
		g2.adminLn.Close()
		g2.store.Close()
	})
	defer flushObservability(t, g2)
	if !g2.otelSet.Limits()["corp"].Paused {
		t.Fatal("the pause did not survive a restart")
	}
}

func TestGatewayConsoleDamagedStateFileRefusesBoot(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	doc := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})
	path := routingAcceptanceConfig(t, &doc, func(cfg map[string]any, d string) {
		withExporters(corp, vault)(cfg, d)
		state := filepath.Join(d, "observability-limits.json")
		if err := os.WriteFile(state, []byte(`{"version":1,"exporters":{"corp":{"pased":true}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["observability"].(map[string]any)["console"] = map[string]any{"state_file": state}
		cfg["server"].(map[string]any)["listen"] = "invalid-listen-address"
	})
	g, err := newGateway(path)
	if g != nil {
		t.Fatal("a gateway with a damaged state file reached serving")
	}
	if err == nil || !strings.Contains(err.Error(), "state_file") {
		t.Fatalf("want a boot error naming the state file, got %v", err)
	}
}

func TestGatewayConsoleRulesLoadThroughPolicyFiles(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	t.Setenv("OBS_CONSOLE_ADMIN", consoleAdmin)
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	doc := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})
	doc.Spec.Subject.Team = "finance" // engineering exports nothing until the console rule
	var consoleFile string
	path := routingAcceptanceConfig(t, &doc, func(cfg map[string]any, d string) {
		withExporters(corp, vault)(cfg, d)
		consoleFile = filepath.Join(d, "console-observability.yaml")
		cfg["policies"] = append(cfg["policies"].([]string), consoleFile)
		cfg["observability"].(map[string]any)["console"] = map[string]any{"policy_file": consoleFile}
		withConsoleAdmin(cfg)
	})
	g, key := bootObservability(t, path)
	if info, err := os.Stat(consoleFile); err != nil || info.Size() != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("console file at boot = %v, %v", info, err)
	}
	_, st := consoleCall(t, g, "GET", "/admin/observability", nil)
	rules := st["rules"].(map[string]any)
	if rules["editable"] != true || st["policy_source"] != "files" {
		t.Fatalf("rules = %v", rules)
	}
	content := map[string]any{"name": "console-eng-content", "subject": map[string]any{"team": "engineering"}, "content": map[string]any{"exporter": "vault"}}
	if code, res := consoleCall(t, g, "PUT", "/admin/observability/rules", map[string]any{"dry_run": true, "expected_revision": rules["revision"], "rules": []any{content}}); code != 400 || res["code"] != "content_not_allowed" {
		t.Fatalf("content without the opt-in = %d %v", code, res)
	}
	rule := map[string]any{"name": "console-eng", "subject": map[string]any{"team": "engineering"}, "audit": map[string]any{"exporter": "corp"}}
	code, res := consoleCall(t, g, "PUT", "/admin/observability/rules", map[string]any{"dry_run": false, "expected_revision": rules["revision"], "rules": []any{rule}})
	if code != 200 {
		t.Fatalf("apply = %d %v", code, res)
	}
	if b, _ := os.ReadFile(consoleFile); !strings.Contains(string(b), "name: console-eng") {
		t.Fatalf("console file = %s", b)
	}
	_, st = consoleCall(t, g, "GET", "/admin/observability", nil)
	found := false
	for _, s := range st["selections"].([]any) {
		sel := s.(map[string]any)
		found = found || (sel["policy"] == "console-eng" && sel["source"] == "console")
	}
	if !found {
		t.Fatalf("selections = %v", st["selections"])
	}
	if rec := routingCall(t, g, key, "console rule"); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	flushObservability(t, g)
	completed := 0
	for _, l := range corp.events("inferplane.audit") {
		if strings.Contains(l.Body.GetStringValue(), `"event":"request_completed"`) {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("engineering audit copies after the console rule = %d", completed)
	}
}

// Review Focus 3: with a control plane, rules are read-only, a limit is still
// editable, and the narrowed signal never rejects a distributed document.
func TestGatewayConsoleReadOnlyUnderControlPlane(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	t.Setenv("OBS_CONSOLE_ADMIN", consoleAdmin)
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	path := routingAcceptanceConfig(t, nil, func(cfg map[string]any, d string) {
		withExporters(corp, vault)(cfg, d)
		cfg["observability"].(map[string]any)["console"] = map[string]any{"state_file": filepath.Join(d, "observability-limits.json")}
		withConsoleAdmin(cfg)
	})
	g, _ := bootObservability(t, path)
	t.Cleanup(func() { flushObservability(t, g) })
	_, st := consoleCall(t, g, "GET", "/admin/observability", nil)
	if st["policy_source"] != "control_plane" || st["rules"].(map[string]any)["reason"] != "control_plane" || st["limits"].(map[string]any)["editable"] != true {
		t.Fatalf("status = %v", st)
	}
	if code, res := consoleCall(t, g, "PUT", "/admin/observability/rules", map[string]any{"dry_run": true, "rules": []any{}}); code != 409 || res["code"] != "read_only" {
		t.Fatalf("rules under a control plane = %d %v", code, res)
	}
	rev := st["limits"].(map[string]any)["revision"]
	if code, res := consoleCall(t, g, "PUT", "/admin/observability/limits", map[string]any{"dry_run": false, "expected_revision": rev,
		"exporters": map[string]any{"corp": map[string]any{"paused": true}}}); code != 200 {
		t.Fatalf("pause = %d %v", code, res)
	}
	mixed := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})
	mixed.Spec.Rules = append(mixed.Spec.Rules, v1alpha1.Rule{Name: "money", FailurePolicy: v1alpha1.FailOpen, Budget: &v1alpha1.BudgetRule{LimitMilliUSD: 10_000}})
	if rej := g.polStore.ApplyWire([]v1alpha1.GovernancePolicy{mixed}); len(rej) != 0 {
		t.Fatalf("a runtime limit made a control-plane document fail: %+v", rej)
	}
	if _, ok := g.polStore.TeamLimits("engineering"); !ok {
		t.Fatal("the budget rule in the same document did not apply")
	}
	selected := g.polStore.Observability("engineering", "").Audit
	if len(selected) != 1 || selected[0] != "corp" {
		t.Fatalf("the policy no longer selects corp, so the pause check below would pass vacuously: %v", selected)
	}
	if got := g.otelSet.AuditTargets(selected); got != nil {
		t.Fatalf("a paused exporter was selected: %v", got)
	}
}

func TestGatewayConsoleWithoutPolicyStore(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	t.Setenv("OBS_CONSOLE_ADMIN", consoleAdmin)
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	path := routingAcceptanceConfig(t, nil, func(cfg map[string]any, d string) {
		delete(cfg, "control_plane") // neither policies nor a control plane: no policy store
		withExporters(corp, vault)(cfg, d)
		withConsoleAdmin(cfg)
	})
	g, _ := bootObservability(t, path)
	t.Cleanup(func() { flushObservability(t, g) })
	if g.polStore != nil {
		t.Fatal("expected no policy store")
	}
	code, st := consoleCall(t, g, "GET", "/admin/observability", nil)
	if code != 200 || st["configured"] != true || st["policy_source"] != "none" || len(st["selections"].([]any)) != 0 {
		t.Fatalf("status %d %v", code, st)
	}
	code, caps := consoleCall(t, g, "GET", "/admin/capabilities", nil)
	if code != 200 || caps["observability_export"] != true {
		t.Fatalf("capabilities %d %v", code, caps)
	}
}

// No observability block: the console is still mounted and says so.
func TestGatewayConsoleWithoutObservabilityBlock(t *testing.T) {
	t.Setenv("OBS_CONSOLE_ADMIN", consoleAdmin)
	path := routingAcceptanceConfig(t, nil, func(cfg map[string]any, _ string) { withConsoleAdmin(cfg) })
	g, _ := bootObservability(t, path)
	t.Cleanup(func() { flushObservability(t, g) })
	code, st := consoleCall(t, g, "GET", "/admin/observability", nil)
	if code != 200 || st["configured"] != false {
		t.Fatalf("status %d %v", code, st)
	}
	code, caps := consoleCall(t, g, "GET", "/admin/capabilities", nil)
	if code != 200 || caps["observability_export"] != false {
		t.Fatalf("capabilities %d %v", code, caps)
	}
}

// ensureConsolePolicyFile only creates: an existing file keeps its bytes.
func TestGatewayConsolePolicyFileSurvivesRestart(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	var consoleFile string
	var keep []byte
	path := routingAcceptanceConfig(t, nil, func(cfg map[string]any, d string) {
		delete(cfg, "control_plane")
		withExporters(corp, vault)(cfg, d)
		consoleFile = filepath.Join(d, "console-observability.yaml")
		doc := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})
		doc.Metadata.Name = "console-keep"
		writeRoutingPolicy(t, consoleFile, doc)
		var err error
		if keep, err = os.ReadFile(consoleFile); err != nil || len(keep) == 0 {
			t.Fatalf("fixture: %v", err)
		}
		cfg["policies"] = []string{consoleFile}
		cfg["observability"].(map[string]any)["console"] = map[string]any{"policy_file": consoleFile}
	})
	g, _ := bootObservability(t, path)
	flushObservability(t, g)
	if b, err := os.ReadFile(consoleFile); err != nil || !bytes.Equal(b, keep) {
		t.Fatalf("console policy file after boot = %q, %v", b, err)
	}
}

type consoleOIDC struct{}

func (consoleOIDC) Verify(context.Context, string) (adminauth.Claims, error) {
	return adminauth.Claims{Issuer: "https://idp.example", Subject: "raw-admin-subject", Groups: []string{"admins"}}, nil
}

// With verified identity configured, an OIDC administrator's change record
// carries a digest identity, never the raw issuer or subject. Removing the
// Organization wiring in observabilityConsole makes this fail.
func TestObservabilityConsoleOIDCAdminRecordedAsDigest(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	raw, _ := json.Marshal(map[string]any{"key_store": map[string]any{
		"type": "sqlite", "path": filepath.Join(dir, "keys.db"),
		"identity": identity.Config{Organization: "org"},
	}})
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openKeysCLI("", cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var sink bytes.Buffer
	aud, err := audit.NewWriter("inst-1", filepath.Join(dir, "a.wal"), []audit.Sink{audit.NewWriterSink("buf", &sink, true)})
	if err != nil {
		t.Fatal(err)
	}
	set, err := otelexport.New("svc", []otelexport.Exporter{{Name: "corp", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"audit"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = set.Shutdown(ctx)
	})
	cfg := &config.Config{Observability: &config.ObservabilityConfig{Console: &config.ObservabilityConsoleConfig{StateFile: filepath.Join(dir, "limits.json")}}}
	h := server.WithFullAdminRoutes(http.NotFoundHandler(), observabilityConsole(cfg, set, nil, aud, store).Routes(), nil,
		consoleOIDC{}, adminauth.MappingConfig{AdminGroups: []string{"admins"}}, aud)
	do := func(method, path string, body any) (int, map[string]any) {
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer verified.oidc.token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: %d %q", method, path, rec.Code, rec.Body)
		}
		return rec.Code, out
	}
	code, st := do("GET", "/admin/observability", nil)
	if code != 200 {
		t.Fatalf("status %d %v", code, st)
	}
	rev := st["limits"].(map[string]any)["revision"]
	if code, res := do("PUT", "/admin/observability/limits", map[string]any{"dry_run": false, "expected_revision": rev,
		"exporters": map[string]any{"corp": map[string]any{"paused": true}}}); code != 200 {
		t.Fatalf("apply %d %v", code, res)
	}
	if err := aud.Close(); err != nil {
		t.Fatal(err)
	}
	out := sink.String()
	var changed *audit.Record
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec audit.Record
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Event == "admin_observability_changed" {
			changed = &rec
		}
	}
	if changed == nil {
		t.Fatalf("no admin_observability_changed record in %q", out)
	}
	if changed.Principal.Identity == nil || changed.Principal.User != nil {
		t.Fatalf("principal = %+v, want a digest identity and no raw user", changed.Principal)
	}
	if strings.Contains(out, "raw-admin-subject") || strings.Contains(out, "idp.example") {
		t.Fatalf("the canonical record carries the raw subject or issuer: %s", out)
	}
}
