package observabilityapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/principal"
)

// put calls one console route as a full administrator and decodes the reply.
func put(t *testing.T, c *Console, pattern string, body any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := c.Routes()[pattern]
	if !ok {
		t.Fatalf("no route %s", pattern)
	}
	method, path, _ := strings.Cut(pattern, " ")
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req = req.WithContext(principal.WithAdmin(req.Context(), principal.AdminIdentity{Subject: "admin-sub", IsAdmin: true, AuthMethod: "oidc"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: status %d, body %q", pattern, rec.Code, rec.Body)
	}
	return rec.Code, out
}

type limitsHarness struct {
	set     *otelexport.Set
	console *Console
	state   string
	records *[]audit.Record
}

func newLimitsHarness(t *testing.T, edit func(*Options)) limitsHarness {
	t.Helper()
	records := &[]audit.Record{}
	opts := Options{Set: newSet(t), PolicySource: SourceNone, StateFile: filepath.Join(t.TempDir(), "observability-limits.json"),
		Emit: func(r audit.Record) { *records = append(*records, r) }}
	if edit != nil {
		edit(&opts)
	}
	return limitsHarness{set: opts.Set, console: New(opts), state: opts.StateFile, records: records}
}

func (h limitsHarness) revision(t *testing.T) string {
	t.Helper()
	st, _ := getStatus(t, h.console)
	return st["limits"].(map[string]any)["revision"].(string)
}

const limitsRoute = "PUT /admin/observability/limits"

func pauseCorp(dryRun bool, revision string) map[string]any {
	return map[string]any{"dry_run": dryRun, "expected_revision": revision, "exporters": map[string]any{"corp": map[string]any{"paused": true}}}
}

func TestLimitsDryRunChangesNothing(t *testing.T) {
	h := newLimitsHarness(t, nil)
	code, res := put(t, h.console, limitsRoute, pauseCorp(true, h.revision(t)))
	if code != 200 || res["dry_run"] != true {
		t.Fatalf("dry run = %d %v", code, res)
	}
	changes := res["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %v", changes)
	}
	if first := changes[0].(map[string]any); first["exporter"] != "corp" || first["field"] != "paused" || first["before"] != false || first["after"] != true {
		t.Fatalf("change = %v", first)
	}
	if _, err := os.Stat(h.state); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the state file: %v", err)
	}
	if len(h.set.Limits()) != 0 || len(*h.records) != 0 {
		t.Fatal("a dry run installed or audited something")
	}
}

func TestLimitsApplyPersistsInstallsAndAudits(t *testing.T) {
	h := newLimitsHarness(t, nil)
	before := h.revision(t)
	code, res := put(t, h.console, limitsRoute, pauseCorp(false, before))
	if code != 200 || res["dry_run"] != false {
		t.Fatalf("apply = %d %v", code, res)
	}
	want := otelexport.EncodeLimits(otelexport.Limits{"corp": {Paused: true}})
	got, err := os.ReadFile(h.state)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("state file = %q (%v), want %q", got, err, want)
	}
	if info, err := os.Stat(h.state); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v (%v)", info.Mode().Perm(), err)
	}
	if !h.set.Limits()["corp"].Paused || res["result_revision"] != otelexport.Revision(want) {
		t.Fatalf("installed = %+v, result = %v", h.set.Limits(), res)
	}
	if len(*h.records) != 1 {
		t.Fatalf("audit records = %d", len(*h.records))
	}
	rec := (*h.records)[0]
	ch := rec.Request.Change
	if rec.Event != "admin_observability_changed" || ch == nil || ch.Target != "observability_limits" || ch.Before != before ||
		ch.After != res["result_revision"] || strings.Join(ch.Names, ",") != "corp" ||
		rec.Principal.User == nil || *rec.Principal.User != "admin-sub" || *rec.Principal.AuthMethod != "oidc" {
		t.Fatalf("audit record = %+v (change %+v)", rec, ch)
	}
	code, res = put(t, h.console, limitsRoute, pauseCorp(false, res["result_revision"].(string)))
	if code != 200 || len(res["changes"].([]any)) != 0 || len(*h.records) != 1 {
		t.Fatalf("a no-op apply = %d %v, records %d", code, res, len(*h.records))
	}
}

func TestLimitsApplyNeedsTheCurrentRevision(t *testing.T) {
	h := newLimitsHarness(t, nil)
	if code, res := put(t, h.console, limitsRoute, pauseCorp(false, "")); code != 400 || res["code"] != "invalid_request" {
		t.Fatalf("apply without a revision = %d %v", code, res)
	}
	code, res := put(t, h.console, limitsRoute, pauseCorp(false, "sha256:stale"))
	if code != 409 || res["code"] != "revision_conflict" || res["current_revision"] != h.revision(t) {
		t.Fatalf("stale apply = %d %v", code, res)
	}
	if len(h.set.Limits()) != 0 || len(*h.records) != 0 {
		t.Fatal("a refused apply changed something")
	}
}

func TestLimitsRefuseWidening(t *testing.T) {
	h := newLimitsHarness(t, nil)
	rev := h.revision(t)
	for name, exporters := range map[string]map[string]any{
		"undeclared exporter": {"other": map[string]any{"paused": true}},
		"undeclared signal":   {"corp": map[string]any{"signals": []string{"content"}}},
		"raised content cap":  {"vault": map[string]any{"max_content_bytes": 2 << 20}},
	} {
		code, res := put(t, h.console, limitsRoute, map[string]any{"dry_run": true, "expected_revision": rev, "exporters": exporters})
		if code != 400 || res["code"] != "invalid_limits" {
			t.Errorf("%s: %d %v", name, code, res)
		}
	}
	if code, res := put(t, h.console, limitsRoute, map[string]any{"dry_run": true, "endpoint": "evil.example:4318"}); code != 400 || res["code"] != "invalid_request" {
		t.Fatalf("an unknown field (an endpoint) = %d %v", code, res)
	}
}

func TestLimitsReadOnlyWithoutStateFile(t *testing.T) {
	h := newLimitsHarness(t, func(o *Options) { o.StateFile = "" })
	if code, res := put(t, h.console, limitsRoute, pauseCorp(true, "")); code != 409 || res["code"] != "read_only" || res["reason"] != "no_state_file" {
		t.Fatalf("read-only = %d %v", code, res)
	}
}

func TestLimitsRefuseAfterAHandEdit(t *testing.T) {
	h := newLimitsHarness(t, nil)
	code, res := put(t, h.console, limitsRoute, pauseCorp(false, h.revision(t)))
	if code != 200 {
		t.Fatalf("apply = %d %v", code, res)
	}
	writeFile(t, h.state, `{"version":1,"exporters":{"vault":{"paused":true}}}`)
	code, res = put(t, h.console, limitsRoute, map[string]any{"dry_run": false, "expected_revision": res["result_revision"], "exporters": map[string]any{}})
	if code != 409 || res["code"] != "limits_file_changed" {
		t.Fatalf("apply over a hand edit = %d %v", code, res)
	}
	if !h.set.Limits()["corp"].Paused {
		t.Fatal("the refused apply changed the installed limits")
	}
}

func TestLimitsRefusedWhileAuditNotReady(t *testing.T) {
	h := newLimitsHarness(t, func(o *Options) { o.AuditReady = func() (bool, string) { return false, "wal" } })
	rev := h.revision(t)
	if code, _ := put(t, h.console, limitsRoute, pauseCorp(true, rev)); code != 200 {
		t.Fatalf("a dry run must still work: %d", code)
	}
	if code, res := put(t, h.console, limitsRoute, pauseCorp(false, rev)); code != 503 || res["code"] != "audit_unavailable" {
		t.Fatalf("apply = %d %v", code, res)
	}
	if _, err := os.Stat(h.state); !os.IsNotExist(err) || len(h.set.Limits()) != 0 {
		t.Fatal("an unrecordable change was made")
	}
}

func TestLimitsEffectsNameWithheldSelections(t *testing.T) {
	h := newLimitsHarness(t, func(o *Options) {
		store, _, _ := consoleStore(t, o.Set, operatorDoc, "")
		o.Policies, o.PolicySource = store, SourceFiles
	})
	code, res := put(t, h.console, limitsRoute, pauseCorp(true, h.revision(t)))
	if code != 200 {
		t.Fatalf("dry run = %d %v", code, res)
	}
	effects := res["effects"].([]any)
	if len(effects) != 2 {
		t.Fatalf("effects = %v", effects)
	}
	auditEffect, tracesEffect := effects[0].(map[string]any), effects[1].(map[string]any)
	if auditEffect["signal"] != "audit" || auditEffect["before"].(map[string]any)["exported"] != true || auditEffect["after"].(map[string]any)["exported"] != false {
		t.Fatalf("audit effect = %v", auditEffect)
	}
	if tracesEffect["signal"] != "traces" || tracesEffect["before"].(map[string]any)["sample_percent"] != float64(20) || tracesEffect["after"].(map[string]any)["exported"] != false {
		t.Fatalf("traces effect = %v", tracesEffect)
	}
}

// Review Focus 2, boot half: a damaged state file refuses boot; a missing one
// means no limits; entries the bootstrap no longer supports are clamped.
func TestLoadLimitsRefusesADamagedFile(t *testing.T) {
	set := newSet(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "observability-limits.json")
	if notes, err := LoadLimits(path, set); err != nil || notes != nil || len(set.Limits()) != 0 {
		t.Fatalf("missing file = %v, %v", notes, err)
	}
	writeFile(t, path, `{"version":1,"exporters":{"corp":{"pased":true}}}`)
	if _, err := LoadLimits(path, set); err == nil || !strings.Contains(err.Error(), "state_file") {
		t.Fatalf("damaged file = %v, want a boot error naming the state file", err)
	}
	writeFile(t, path, `{"version":1,"exporters":{"corp":{"paused":true},"gone":{"paused":true}}}`)
	notes, err := LoadLimits(path, set)
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "gone") || !set.Limits()["corp"].Paused {
		t.Fatalf("clamped load = %v, %v, limits %+v", notes, err, set.Limits())
	}
	if _, err := LoadLimits(filepath.Join(dir, "missing-dir", "limits.json"), set); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("missing directory = %v", err)
	}
}

func rawPut(t *testing.T, c *Console, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("PUT", "/admin/observability/limits", strings.NewReader(body))
	req = req.WithContext(principal.WithAdmin(req.Context(), principal.AdminIdentity{Subject: "admin-sub", IsAdmin: true, AuthMethod: "oidc"}))
	rec := httptest.NewRecorder()
	c.Routes()[limitsRoute].ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body)
	}
	return rec.Code, out
}

func TestLimitsRefuseTrailingData(t *testing.T) {
	h := newLimitsHarness(t, nil)
	for name, body := range map[string]string{
		"second object":    `{"dry_run":true} {"dry_run":false}`,
		"trailing brace":   `{"dry_run":true}}`,
		"trailing bracket": `{"dry_run":true}]`,
	} {
		if code, res := rawPut(t, h.console, body); code != 400 || res["code"] != "invalid_request" {
			t.Errorf("%s: %d %v", name, code, res)
		}
	}
}

func failInstall(t *testing.T) {
	t.Helper()
	orig := installLimits
	installLimits = func(*otelexport.Set, otelexport.Limits) error { return errors.New("boom") }
	t.Cleanup(func() { installLimits = orig })
}

func TestLimitsInstallFailureRestoresTheStateFile(t *testing.T) {
	h := newLimitsHarness(t, nil)
	prior := otelexport.EncodeLimits(nil)
	if err := os.WriteFile(h.state, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	failInstall(t)
	code, res := put(t, h.console, limitsRoute, pauseCorp(false, h.revision(t)))
	if code != 500 || res["code"] != "persist_failed" {
		t.Fatalf("apply = %d %v", code, res)
	}
	if got, err := os.ReadFile(h.state); err != nil || !bytes.Equal(got, prior) {
		t.Fatalf("state file = %q (%v), want the pre-request bytes %q", got, err, prior)
	}
	if len(h.set.Limits()) != 0 || len(*h.records) != 0 {
		t.Fatal("a failed install changed limits or audited")
	}
}

func TestLimitsInstallFailureRemovesANewStateFile(t *testing.T) {
	h := newLimitsHarness(t, nil)
	failInstall(t)
	code, res := put(t, h.console, limitsRoute, pauseCorp(false, h.revision(t)))
	if code != 500 || res["code"] != "persist_failed" {
		t.Fatalf("apply = %d %v", code, res)
	}
	if _, err := os.Stat(h.state); !os.IsNotExist(err) {
		t.Fatalf("state file remains: %v", err)
	}
	if len(h.set.Limits()) != 0 || len(*h.records) != 0 {
		t.Fatal("a failed install changed limits or audited")
	}
}
