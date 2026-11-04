package observabilityapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/otelexport"
	"github.com/inferplane/inferplane/internal/policy"
)

const operatorDoc = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: engineering-observability
spec:
  subject:
    team: engineering
  rules:
  - name: export
    failurePolicy: FailOpen
    observability:
      traces: { exporter: corp, samplePercent: 20 }
      audit: { exporter: corp }
`

const consoleDoc = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: console-platform
spec:
  subject:
    team: platform
  rules:
  - name: export
    failurePolicy: FailOpen
    observability:
      audit: { exporter: corp }
`

const foreignDoc = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: console-money
spec:
  subject:
    team: platform
  rules:
  - name: money
    failurePolicy: FailOpen
    budget:
      limitMilliUSD: 10000
`

func newSet(t *testing.T) *otelexport.Set {
	t.Helper()
	s, err := otelexport.New("console-test", []otelexport.Exporter{
		{Name: "corp", Endpoint: "127.0.0.1:1", Insecure: true, Headers: map[string]string{"authorization": "Bearer console-secret"}, Signals: []string{"traces", "audit"}},
		{Name: "vault", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"content"}, DataBoundary: "internal", MaxContentBytes: 1 << 20, QueueBytes: 4 << 20},
	}, metrics.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// consoleStore loads an operator policy file and the console file through a
// real policy.Store with the set's ceiling, the way mayu assembles them.
func consoleStore(t *testing.T, set *otelexport.Set, operator, console string) (store *policy.Store, operatorPath, consolePath string) {
	t.Helper()
	dir := t.TempDir()
	operatorPath, consolePath = filepath.Join(dir, "operator.yaml"), filepath.Join(dir, "console-observability.yaml")
	writeFile(t, operatorPath, operator)
	writeFile(t, consolePath, console)
	store, err := policy.NewStore(operatorPath, consolePath)
	if err != nil {
		t.Fatal(err)
	}
	store.SetObservabilityCeiling(set.Allows)
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	return store, operatorPath, consolePath
}

func getStatus(t *testing.T, c *Console) (map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c.Routes()["GET /admin/observability"].ServeHTTP(rec, httptest.NewRequest("GET", "/admin/observability", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out, rec.Body.String()
}

func TestStatusWithoutExportersOrPolicyStore(t *testing.T) {
	st, _ := getStatus(t, New(Options{}))
	if st["configured"] != false || st["policy_source"] != "none" {
		t.Fatalf("status = %v", st)
	}
	for _, part := range []string{"limits", "rules"} {
		if e := st[part].(map[string]any); e["editable"] != false || e["reason"] != "not_configured" {
			t.Fatalf("%s = %v", part, e)
		}
	}
	for _, k := range []string{"exporters", "decisions", "selections"} {
		if v, ok := st[k].([]any); !ok || len(v) != 0 {
			t.Fatalf("%s = %v, want an empty list", k, st[k])
		}
	}
}

func TestStatusReportsExportersSelectionsAndConsoleRules(t *testing.T) {
	set := newSet(t)
	if _, err := set.SetLimits(otelexport.Limits{"corp": {MaxSamplePercent: 10}}); err != nil {
		t.Fatal(err)
	}
	store, _, consolePath := consoleStore(t, set, operatorDoc, consoleDoc)
	c := New(Options{Set: set, Policies: store, PolicySource: SourceFiles, StateFile: filepath.Join(t.TempDir(), "limits.json"),
		PolicyFile: consolePath, Legacy: &LegacyExporter{Endpoint: "localhost:4318", Protocol: "http", SampleRatio: 0.25}})
	st, raw := getStatus(t, c)
	if strings.Contains(raw, "console-secret") {
		t.Fatalf("status leaked a header value: %s", raw)
	}
	if st["configured"] != true || st["policy_source"] != "files" || st["service_name"] != "console-test" {
		t.Fatalf("status = %v", st)
	}
	exporters := st["exporters"].([]any)
	corp := exporters[0].(map[string]any)
	if len(exporters) != 2 || corp["name"] != "corp" || corp["header_names"].([]any)[0] != "authorization" {
		t.Fatalf("exporters = %v", exporters)
	}
	limits := st["limits"].(map[string]any)
	if limits["editable"] != true || limits["revision"] != otelexport.Revision(otelexport.EncodeLimits(set.Limits())) {
		t.Fatalf("limits = %v", limits)
	}
	rules := st["rules"].(map[string]any)
	fileBytes, _ := os.ReadFile(consolePath)
	console := rules["console"].([]any)
	if rules["editable"] != true || rules["revision"] != otelexport.Revision(fileBytes) || len(console) != 1 ||
		console[0].(map[string]any)["name"] != "console-platform" {
		t.Fatalf("rules = %v", rules)
	}
	sources := map[string]string{}
	var engineering map[string]any
	for _, s := range st["selections"].([]any) {
		sel := s.(map[string]any)
		sources[sel["policy"].(string)] = sel["source"].(string)
		if sel["policy"] == "engineering-observability" {
			engineering = sel
		}
	}
	if sources["console-platform"] != "console" || sources["engineering-observability"] != "files" {
		t.Fatalf("selection sources = %v", sources)
	}
	traces := engineering["targets"].([]any)[0].(map[string]any)
	if traces["signal"] != "traces" || traces["sample_percent"] != float64(20) || traces["effective_percent"] != float64(10) || traces["exported"] != true {
		t.Fatalf("engineering traces target = %v", traces)
	}
	legacy := st["legacy_otel"].(map[string]any)
	if legacy["endpoint"] != "localhost:4318" || legacy["sample_ratio"] != 0.25 || legacy["stats"].(map[string]any)["signal"] != "traces" {
		t.Fatalf("legacy = %v", legacy)
	}
}

func TestStatusRuleReadOnlyReasons(t *testing.T) {
	set := newSet(t)
	store, _, consolePath := consoleStore(t, set, operatorDoc, "")
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"control plane", Options{Set: set, Policies: store, PolicySource: SourceControlPlane}, "control_plane"},
		{"no policy file", Options{Set: set, Policies: store, PolicySource: SourceFiles}, "no_policy_file"},
		{"no state file for limits", Options{Set: set, Policies: store, PolicySource: SourceFiles, PolicyFile: consolePath}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := getStatus(t, New(tc.opts))
			rules := st["rules"].(map[string]any)
			if tc.want == "" {
				if rules["editable"] != true || st["limits"].(map[string]any)["reason"] != "no_state_file" {
					t.Fatalf("rules = %v, limits = %v", rules, st["limits"])
				}
				return
			}
			if rules["editable"] != false || rules["reason"] != tc.want {
				t.Fatalf("rules = %v, want reason %s", rules, tc.want)
			}
		})
	}
	foreignStore, _, foreignPath := consoleStore(t, set, operatorDoc, foreignDoc)
	st, _ := getStatus(t, New(Options{Set: set, Policies: foreignStore, PolicySource: SourceFiles, PolicyFile: foreignPath}))
	if rules := st["rules"].(map[string]any); rules["editable"] != false || rules["reason"] != "foreign_documents" || len(rules["console"].([]any)) != 0 {
		t.Fatalf("foreign console file: rules = %v", rules)
	}
}

// The status names the answering process (the audit chain's instance id), so
// an administrator behind a load balancer can tell which replica's limits and
// counters it shows. An unset instance is omitted.
func TestStatusNamesTheAnsweringInstance(t *testing.T) {
	st, _ := getStatus(t, New(Options{Set: newSet(t), Instance: "gw-a-01JINSTANCE"}))
	if st["instance"] != "gw-a-01JINSTANCE" {
		t.Fatalf("instance = %v", st["instance"])
	}
	st, _ = getStatus(t, New(Options{}))
	if v, has := st["instance"]; has {
		t.Fatalf("an unset instance must be omitted, got %v", v)
	}
}

// D11: without an observability block there are no counters for the legacy
// exporter, so its row carries no stats rather than a misleading zero.
func TestStatusLegacyWithoutSetHasNoStats(t *testing.T) {
	c := New(Options{Legacy: &LegacyExporter{Endpoint: "localhost:4318", Protocol: "http", SampleRatio: 0.5}})
	st, _ := getStatus(t, c)
	legacy, ok := st["legacy_otel"].(map[string]any)
	if !ok || legacy["endpoint"] != "localhost:4318" || legacy["protocol"] != "http" || legacy["sample_ratio"] != 0.5 {
		t.Fatalf("legacy = %v", st["legacy_otel"])
	}
	if _, has := legacy["stats"]; has {
		t.Fatalf("legacy row must have no stats key without a Set: %v", legacy)
	}
}
