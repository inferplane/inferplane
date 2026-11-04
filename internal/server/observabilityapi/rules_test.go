package observabilityapi

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

const rulesRoute = "PUT /admin/observability/rules"

const consoleContentDoc = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: console-vault
spec:
  subject:
    team: engineering
  rules:
  - name: export
    failurePolicy: FailOpen
    observability:
      content: { exporter: vault }
`

type rulesHarness struct {
	store        *policy.Store
	console      *Console
	operatorPath string
	consolePath  string
	records      *[]audit.Record
}

func newRulesHarness(t *testing.T, console string, edit func(*Options)) rulesHarness {
	t.Helper()
	set := newSet(t)
	store, operatorPath, consolePath := consoleStore(t, set, operatorDoc, console)
	records := &[]audit.Record{}
	opts := Options{Set: set, Policies: store, PolicySource: SourceFiles, PolicyFile: consolePath,
		Emit: func(r audit.Record) { *records = append(*records, r) }}
	if edit != nil {
		edit(&opts)
	}
	return rulesHarness{store: store, console: New(opts), operatorPath: operatorPath, consolePath: consolePath, records: records}
}

func (h rulesHarness) revision(t *testing.T) string {
	t.Helper()
	st, _ := getStatus(t, h.console)
	rev, _ := st["rules"].(map[string]any)["revision"].(string)
	return rev
}

func financeRule() map[string]any {
	return map[string]any{"name": "console-finance", "subject": map[string]any{"team": "finance"}, "audit": map[string]any{"exporter": "corp"}}
}

func rulesBody(dryRun bool, revision string, rules ...map[string]any) map[string]any {
	if rules == nil {
		rules = []map[string]any{}
	}
	return map[string]any{"dry_run": dryRun, "expected_revision": revision, "rules": rules}
}

func policyNames(s *policy.Store) string {
	var names []string
	for _, p := range s.Policies() {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestRulesDryRunShowsTheAddedRuleAndDecision(t *testing.T) {
	h := newRulesHarness(t, "", nil)
	code, res := put(t, h.console, rulesRoute, rulesBody(true, h.revision(t), financeRule()))
	if code != 200 || res["dry_run"] != true {
		t.Fatalf("dry run = %d %v", code, res)
	}
	changes := res["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["name"] != "console-finance" || changes[0].(map[string]any)["action"] != "added" {
		t.Fatalf("changes = %v", changes)
	}
	decisions := res["decisions"].([]any)
	if len(decisions) != 1 {
		t.Fatalf("decisions = %v", decisions)
	}
	d := decisions[0].(map[string]any)
	if d["subject"].(map[string]any)["team"] != "finance" || len(d["before"].(map[string]any)["audit"].([]any)) != 0 ||
		d["after"].(map[string]any)["audit"].([]any)[0] != "corp" {
		t.Fatalf("decision = %v", d)
	}
	if b, _ := os.ReadFile(h.consolePath); len(b) != 0 || policyNames(h.store) != "engineering-observability" {
		t.Fatal("a dry run wrote the console file or changed the policy set")
	}
}

func TestRulesApplyWritesTheFileReloadsAndAudits(t *testing.T) {
	h := newRulesHarness(t, "", nil)
	before := h.revision(t)
	code, res := put(t, h.console, rulesRoute, rulesBody(false, before, financeRule()))
	if code != 200 {
		t.Fatalf("apply = %d %v", code, res)
	}
	b, err := os.ReadFile(h.consolePath)
	if err != nil {
		t.Fatal(err)
	}
	if res["result_revision"] != otelexport.Revision(b) {
		t.Fatalf("result revision %v is not the file digest", res["result_revision"])
	}
	docs, err := policy.ParseWireDocs(b)
	if err != nil || len(docs) != 1 || docs[0].Metadata.Name != "console-finance" {
		t.Fatalf("console file = %s (%v)", b, err)
	}
	if got := policyNames(h.store); got != "console-finance,engineering-observability" {
		t.Fatalf("live policy set = %s", got)
	}
	if d := h.store.Observability("finance", ""); strings.Join(d.Audit, ",") != "corp" {
		t.Fatalf("finance decision after reload = %+v", d)
	}
	if len(*h.records) != 1 {
		t.Fatalf("audit records = %d", len(*h.records))
	}
	ch := (*h.records)[0].Request.Change
	if ch == nil || ch.Target != "observability_rules" || ch.Before != before || ch.Before != otelexport.Revision(nil) ||
		ch.After != res["result_revision"] || strings.Join(ch.Names, ",") != "console-finance" {
		t.Fatalf("change = %+v", ch)
	}
	st, _ := getStatus(t, h.console)
	found := false
	for _, s := range st["selections"].([]any) {
		sel := s.(map[string]any)
		found = found || (sel["policy"] == "console-finance" && sel["source"] == "console")
	}
	if !found {
		t.Fatalf("selections = %v", st["selections"])
	}
}

func TestRulesValidation(t *testing.T) {
	h := newRulesHarness(t, "", nil)
	rev := h.revision(t)
	for name, rule := range map[string]map[string]any{
		"name without prefix": {"name": "finance", "subject": map[string]any{"team": "finance"}, "audit": map[string]any{"exporter": "corp"}},
		"no subject":          {"name": "console-x", "subject": map[string]any{}, "audit": map[string]any{"exporter": "corp"}},
		"no signal":           {"name": "console-x", "subject": map[string]any{"team": "finance"}},
		"percent too high":    {"name": "console-x", "subject": map[string]any{"team": "finance"}, "traces": map[string]any{"exporter": "corp", "sample_percent": 101}},
		"undeclared exporter": {"name": "console-x", "subject": map[string]any{"team": "finance"}, "audit": map[string]any{"exporter": "missing"}},
		"signal not allowed":  {"name": "console-x", "subject": map[string]any{"team": "finance"}, "audit": map[string]any{"exporter": "vault"}},
		"team with a slash":   {"name": "console-x", "subject": map[string]any{"team": "fin/ance"}, "audit": map[string]any{"exporter": "corp"}},
	} {
		if code, res := put(t, h.console, rulesRoute, rulesBody(true, rev, rule)); code != 400 || res["code"] != "invalid_rules" {
			t.Errorf("%s: %d %v", name, code, res)
		}
	}
	if code, res := put(t, h.console, rulesRoute, rulesBody(true, rev, financeRule(), financeRule())); code != 400 || !strings.Contains(res["error"].(string), "duplicate") {
		t.Fatalf("duplicate names = %d %v", code, res)
	}
	if code, res := put(t, h.console, rulesRoute, rulesBody(false, "sha256:stale", financeRule())); code != 409 || res["code"] != "revision_conflict" {
		t.Fatalf("stale apply = %d %v", code, res)
	}
}

func TestRulesNameMayNotShadowAnotherFile(t *testing.T) {
	set := newSet(t)
	store, _, consolePath := consoleStore(t, set, strings.Replace(operatorDoc, "name: engineering-observability", "name: console-shared", 1), "")
	c := New(Options{Set: set, Policies: store, PolicySource: SourceFiles, PolicyFile: consolePath})
	rule := map[string]any{"name": "console-shared", "subject": map[string]any{"team": "finance"}, "audit": map[string]any{"exporter": "corp"}}
	if code, res := put(t, c, rulesRoute, rulesBody(true, "", rule)); code != 400 || !strings.Contains(res["error"].(string), "another policy file") {
		t.Fatalf("shadowing an operator document = %d %v", code, res)
	}
}

func TestRulesContentNeedsTheBootstrapOptIn(t *testing.T) {
	content := map[string]any{"name": "console-vault", "subject": map[string]any{"team": "engineering"}, "content": map[string]any{"exporter": "vault"}}
	h := newRulesHarness(t, "", nil)
	if code, res := put(t, h.console, rulesRoute, rulesBody(true, h.revision(t), content)); code != 400 || res["code"] != "content_not_allowed" {
		t.Fatalf("new content selection without opt-in = %d %v", code, res)
	}
	kept := newRulesHarness(t, consoleContentDoc, nil)
	rev := kept.revision(t)
	if code, res := put(t, kept.console, rulesRoute, rulesBody(true, rev, content, financeRule())); code != 200 {
		t.Fatalf("keeping an unchanged content selection = %d %v", code, res)
	}
	moved := map[string]any{"name": "console-vault", "subject": map[string]any{"team": "finance"}, "content": map[string]any{"exporter": "vault"}}
	if code, res := put(t, kept.console, rulesRoute, rulesBody(true, rev, moved)); code != 400 || res["code"] != "content_not_allowed" {
		t.Fatalf("moving a content selection to another subject = %d %v", code, res)
	}
	if code, res := put(t, kept.console, rulesRoute, rulesBody(true, rev)); code != 200 || res["summary"].(map[string]any)["removed"] != float64(1) {
		t.Fatalf("removing a content selection = %d %v", code, res)
	}
	opted := newRulesHarness(t, "", func(o *Options) { o.ContentRules = true })
	if code, res := put(t, opted.console, rulesRoute, rulesBody(true, opted.revision(t), content)); code != 200 {
		t.Fatalf("content with content_rules = %d %v", code, res)
	}
}

func TestRulesReadOnlyAndForeign(t *testing.T) {
	cp := newRulesHarness(t, "", func(o *Options) { o.PolicySource = SourceControlPlane })
	if code, res := put(t, cp.console, rulesRoute, rulesBody(true, "", financeRule())); code != 409 || res["code"] != "read_only" || res["reason"] != "control_plane" {
		t.Fatalf("control plane = %d %v", code, res)
	}
	foreign := newRulesHarness(t, foreignDoc, nil)
	before, _ := os.ReadFile(foreign.consolePath)
	if code, res := put(t, foreign.console, rulesRoute, rulesBody(false, otelexport.Revision(before), financeRule())); code != 409 || res["code"] != "console_file_foreign" {
		t.Fatalf("foreign console file = %d %v", code, res)
	}
	if after, _ := os.ReadFile(foreign.consolePath); string(after) != string(before) {
		t.Fatal("the console overwrote a file it does not own")
	}
}

// Review Focus 4: when Reload fails after the write, the console file is
// restored, so disk matches the running set.
func TestRulesApplyRestoresTheFileWhenReloadFails(t *testing.T) {
	h := newRulesHarness(t, consoleDoc, nil)
	before, _ := os.ReadFile(h.consolePath)
	rev := h.revision(t)
	writeFile(t, h.operatorPath, "spec: [") // an operator's broken edit, not yet reloaded
	code, res := put(t, h.console, rulesRoute, rulesBody(false, rev, financeRule()))
	if code != 409 || res["code"] != "policy_reload_failed" || !strings.Contains(res["error"].(string), "operator.yaml") {
		t.Fatalf("apply over a broken operator file = %d %v", code, res)
	}
	if after, _ := os.ReadFile(h.consolePath); string(after) != string(before) {
		t.Fatalf("console file not restored:\n%s", after)
	}
	if got := policyNames(h.store); got != "console-platform,engineering-observability" {
		t.Fatalf("live policy set changed: %s", got)
	}
	if len(*h.records) != 0 {
		t.Fatal("a failed apply was audited as a change")
	}
}

func TestEncodeRulesRoundTrips(t *testing.T) {
	rules, err := normalizeRules([]Rule{
		{Name: "console-b", Subject: Subject{User: "alice"}, Traces: &TraceTarget{Exporter: "corp"}},
		{Name: "console-a", Subject: Subject{Team: "eng"}, Audit: &SignalTarget{Exporter: "corp"}, Content: &SignalTarget{Exporter: "vault"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, policies, err := encodeRules(rules)
	if err != nil || len(policies) != 2 {
		t.Fatalf("encode = %v (%d policies)", err, len(policies))
	}
	docs, err := policy.ParseWireDocs(b)
	if err != nil || len(docs) != 2 || docs[0].Metadata.Name != "console-a" {
		t.Fatalf("file = %s (%v)", b, err)
	}
	for i, d := range docs {
		if back, err := ruleFromDoc(d); err != nil || back.Name != rules[i].Name {
			t.Fatalf("round trip %d = %+v (%v)", i, back, err)
		}
	}
	if p := docs[1].Spec.Rules[0].Observability.Traces.SamplePercent; p == nil || *p != 100 {
		t.Fatal("an omitted sample percent must be written explicitly as 100")
	}
	if empty, _, _ := encodeRules(nil); len(empty) != 0 {
		t.Fatalf("no rules = %q, want an empty file", empty)
	}
}

// Ruling 2: an organization lookup error refuses the apply before anything is
// written.
func TestRulesIdentityLookupErrorRefusesTheApply(t *testing.T) {
	h := newRulesHarness(t, consoleDoc, func(o *Options) {
		o.Organization = func(context.Context) (string, error) { return "", errors.New("down") }
	})
	before, _ := os.ReadFile(h.consolePath)
	code, res := put(t, h.console, rulesRoute, rulesBody(false, h.revision(t), financeRule()))
	if code != 503 || res["code"] != "audit_unavailable" {
		t.Fatalf("apply = %d %v", code, res)
	}
	if after, _ := os.ReadFile(h.consolePath); string(after) != string(before) || len(*h.records) != 0 {
		t.Fatal("an unrecordable change was made")
	}
}

// A console file that did not exist before the apply is removed again when the
// reload fails, not rewritten as an empty file.
func TestRulesApplyRemovesANewFileWhenReloadFails(t *testing.T) {
	h := newRulesHarness(t, "", nil)
	rev := h.revision(t)
	if err := os.Remove(h.consolePath); err != nil {
		t.Fatal(err)
	}
	writeFile(t, h.operatorPath, "spec: [")
	code, res := put(t, h.console, rulesRoute, rulesBody(false, rev, financeRule()))
	if code != 409 || res["code"] != "policy_reload_failed" {
		t.Fatalf("apply = %d %v", code, res)
	}
	if _, err := os.Stat(h.consolePath); !os.IsNotExist(err) {
		t.Fatalf("the new console file was left behind (stat err %v)", err)
	}
}

// When the restore write itself fails, the answer says the file still holds
// the unapplied rules, and no change is recorded.
func TestRulesRestoreFailureIsReportedHonestly(t *testing.T) {
	h := newRulesHarness(t, consoleDoc, nil)
	rev := h.revision(t)
	writeFile(t, h.operatorPath, "spec: [")
	calls := 0
	orig := writeAtomic
	writeAtomic = func(path string, data []byte) error {
		calls++
		if calls == 2 {
			return errors.New("disk gone")
		}
		return orig(path, data)
	}
	t.Cleanup(func() { writeAtomic = orig })
	code, res := put(t, h.console, rulesRoute, rulesBody(false, rev, financeRule()))
	msg, _ := res["error"].(string)
	if code != 409 || res["code"] != "policy_reload_failed" || !strings.Contains(msg, "still holds the unapplied rules") || strings.Contains(msg, "was restored") {
		t.Fatalf("apply = %d %v", code, res)
	}
	if b, _ := os.ReadFile(h.consolePath); !strings.Contains(string(b), "console-finance") {
		t.Fatal("expected the unrestored file to hold the new rules")
	}
	if len(*h.records) != 0 {
		t.Fatal("a failed apply was audited as a change")
	}
}

func TestRulesPersistFailure(t *testing.T) {
	h := newRulesHarness(t, consoleDoc, nil)
	before, _ := os.ReadFile(h.consolePath)
	rev := h.revision(t)
	orig := writeAtomic
	writeAtomic = func(string, []byte) error { return errors.New("read-only filesystem") }
	t.Cleanup(func() { writeAtomic = orig })
	code, res := put(t, h.console, rulesRoute, rulesBody(false, rev, financeRule()))
	if code != 500 || res["code"] != "persist_failed" {
		t.Fatalf("apply = %d %v", code, res)
	}
	if after, _ := os.ReadFile(h.consolePath); string(after) != string(before) || len(*h.records) != 0 {
		t.Fatal("a failed write changed state")
	}
}

func TestRulesAuditNotReadyRefusesTheApply(t *testing.T) {
	h := newRulesHarness(t, consoleDoc, func(o *Options) {
		o.AuditReady = func() (bool, string) { return false, "wal" }
	})
	before, _ := os.ReadFile(h.consolePath)
	code, res := put(t, h.console, rulesRoute, rulesBody(false, h.revision(t), financeRule()))
	if code != 503 || res["code"] != "audit_unavailable" {
		t.Fatalf("apply = %d %v", code, res)
	}
	if after, _ := os.ReadFile(h.consolePath); string(after) != string(before) || len(*h.records) != 0 {
		t.Fatal("an unrecordable change was made")
	}
}

// The same rules in another byte layout are a no-op: no write, no reload, no
// record, and the result revision stays the current one.
func TestRulesSemanticNoOp(t *testing.T) {
	h := newRulesHarness(t, consoleDoc, nil)
	before, _ := os.ReadFile(h.consolePath)
	rev := h.revision(t)
	same := map[string]any{"name": "console-platform", "subject": map[string]any{"team": "platform"}, "audit": map[string]any{"exporter": "corp"}}
	for _, dry := range []bool{true, false} {
		code, res := put(t, h.console, rulesRoute, rulesBody(dry, rev, same))
		if code != 200 || res["result_revision"] != rev || len(res["changes"].([]any)) != 0 {
			t.Fatalf("dry=%v: %d %v", dry, code, res)
		}
	}
	if after, _ := os.ReadFile(h.consolePath); string(after) != string(before) || len(*h.records) != 0 {
		t.Fatal("a no-op rewrote the file or was recorded")
	}
}
