package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/tracing"
)

// fakeOTLP is an OTLP/HTTP receiver for both traces and logs.
type fakeOTLP struct {
	mu    sync.Mutex
	srv   *httptest.Server
	logs  []*logspb.LogRecord
	spans []string
	auth  []string
}

func newFakeOTLP(t *testing.T) *fakeOTLP {
	t.Helper()
	f := &fakeOTLP{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/logs":
			req := &collogspb.ExportLogsServiceRequest{}
			if proto.Unmarshal(body, req) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, rl := range req.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					f.logs = append(f.logs, sl.LogRecords...)
				}
			}
		case "/v1/traces":
			req := &coltracepb.ExportTraceServiceRequest{}
			if proto.Unmarshal(body, req) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, rs := range req.ResourceSpans {
				for _, ss := range rs.ScopeSpans {
					for _, s := range ss.Spans {
						f.spans = append(f.spans, s.Name)
					}
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOTLP) endpoint() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func (f *fakeOTLP) events(name string) []*logspb.LogRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*logspb.LogRecord
	for _, l := range f.logs {
		if l.EventName == name {
			out = append(out, l)
		}
	}
	return out
}

func (f *fakeOTLP) spanNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.spans...)
}

func observabilityDoc(rule *v1alpha1.ObservabilityRule) v1alpha1.GovernancePolicy {
	return v1alpha1.GovernancePolicy{
		TypeMeta: v1alpha1.TypeMeta{APIVersion: v1alpha1.APIVersion, Kind: v1alpha1.KindGovernancePolicy},
		Metadata: v1alpha1.ObjectMeta{Name: "observability", Generation: 1},
		Spec: v1alpha1.GovernancePolicySpec{Subject: v1alpha1.Subject{Team: "engineering"}, Rules: []v1alpha1.Rule{{
			Name: "export", FailurePolicy: v1alpha1.FailOpen, Observability: rule,
		}}},
	}
}

func withExporters(corp, vault *fakeOTLP) func(map[string]any, string) {
	return func(cfg map[string]any, _ string) {
		cfg["observability"] = map[string]any{"service_name": "mayu-acceptance", "exporters": map[string]any{
			"corp": map[string]any{"endpoint": corp.endpoint(), "insecure": true, "signals": []string{"traces", "audit"},
				"headers": map[string]any{"authorization": map[string]any{"env": "OBS_ACCEPTANCE_TOKEN"}}},
			"vault": map[string]any{"endpoint": vault.endpoint(), "insecure": true, "signals": []string{"content"}, "data_boundary": "internal"},
		}}
	}
}

// bootObservability boots without the shared cleanup's audit close, because
// these tests close the audit writer themselves to flush its last records.
func bootObservability(t *testing.T, path string) (*gateway, string) {
	t.Helper()
	g, err := newGateway(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		g.dataLn.Close()
		g.adminLn.Close()
		g.store.Close()
	})
	key, _, err := g.store.Create(context.Background(), "engineering", []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	return g, key
}

// flushObservability drains the audit writer, then the exporters, the same
// order serve uses on shutdown.
func flushObservability(t *testing.T, g *gateway) {
	t.Helper()
	if err := g.aud.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if g.otelSet != nil {
		if err := g.otelSet.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if g.otelDown != nil {
		if err := g.otelDown(ctx); err != nil {
			t.Fatal(err)
		}
		tracing.Disable() // the provider is process-global; later tests start clean
	}
}

func TestGatewayObservabilityExportsGovernedSignals(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	sample := 100
	doc := observabilityDoc(&v1alpha1.ObservabilityRule{
		Traces:  &v1alpha1.TraceExport{Exporter: "corp", SamplePercent: &sample},
		Audit:   &v1alpha1.SignalExport{Exporter: "corp"},
		Content: &v1alpha1.SignalExport{Exporter: "vault"},
	})
	g, key := bootObservability(t, routingAcceptanceConfig(t, &doc, withExporters(corp, vault)))
	if rec := routingCall(t, g, key, "summarize the release notes"); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	flushObservability(t, g)

	if spans := corp.spanNames(); len(spans) != 1 || !strings.HasPrefix(spans[0], "chat ") {
		t.Fatalf("corp spans = %v", spans)
	}
	audits := corp.events("inferplane.audit")
	var completed int
	for _, l := range audits {
		if strings.Contains(l.Body.GetStringValue(), `"event":"request_completed"`) {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("corp audit copies = %d request_completed of %d", completed, len(audits))
	}
	contents := vault.events("inferplane.content")
	if len(contents) != 1 {
		t.Fatalf("vault content records = %d", len(contents))
	}
	var request string
	for _, kv := range contents[0].Body.GetKvlistValue().GetValues() {
		if kv.Key == "request" {
			request = kv.Value.GetStringValue()
		}
	}
	if !strings.Contains(request, "summarize the release notes") {
		t.Fatalf("content request = %q", request)
	}
	if len(corp.events("inferplane.content")) != 0 || len(vault.events("inferplane.audit")) != 0 || len(vault.spanNames()) != 0 {
		t.Fatal("a signal reached an exporter the policy did not select")
	}
	for _, a := range corp.auth {
		if a != "Bearer acceptance-collector" {
			t.Fatalf("corp received authorization %q", a)
		}
	}
}

func TestGatewayObservabilityOtherTeamExportsNothing(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	doc := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})
	doc.Spec.Subject.Team = "finance"
	g, key := bootObservability(t, routingAcceptanceConfig(t, &doc, withExporters(corp, vault)))
	if rec := routingCall(t, g, key, "hello"); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	flushObservability(t, g)
	if n := len(corp.events("inferplane.audit")); n != 0 {
		t.Fatalf("engineering traffic exported %d audit copies under a finance-only policy", n)
	}
}

func TestGatewayObservabilityRejectsUndeclaredExporter(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	doc := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "missing"}})
	g, err := newGateway(routingAcceptanceConfig(t, &doc, func(cfg map[string]any, dir string) {
		withExporters(corp, vault)(cfg, dir)
		cfg["server"].(map[string]any)["listen"] = "invalid-listen-address"
	}))
	if g != nil {
		t.Fatal("a policy naming an undeclared exporter reached a serving gateway")
	}
	if err == nil || !strings.Contains(err.Error(), "policies:") || !strings.Contains(err.Error(), `"missing"`) {
		t.Fatalf("want rejection naming the exporter, got %v", err)
	}
}

func TestGatewayObservabilityCeilingAppliesToControlPlane(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	g, _ := bootObservability(t, routingAcceptanceConfig(t, nil, withExporters(corp, vault)))
	t.Cleanup(func() { flushObservability(t, g) })
	rej := g.polStore.ApplyWire([]v1alpha1.GovernancePolicy{observabilityDoc(&v1alpha1.ObservabilityRule{Content: &v1alpha1.SignalExport{Exporter: "corp"}})})
	if len(rej) != 1 || !strings.Contains(rej[0].Reason, "does not allow content") {
		t.Fatalf("control-plane document outside the ceiling: rejections = %+v", rej)
	}
}

func TestGatewayObservabilityWithoutCeilingRejectsRules(t *testing.T) {
	g, _ := bootObservability(t, routingAcceptanceConfig(t, nil, nil))
	t.Cleanup(func() { flushObservability(t, g) })
	rej := g.polStore.ApplyWire([]v1alpha1.GovernancePolicy{observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}})})
	if len(rej) != 1 || !strings.Contains(rej[0].Reason, "no observability exporters") {
		t.Fatalf("a data plane without a ceiling must refuse, not silently accept: %+v", rej)
	}
}

func TestGatewayLegacyOTelStillExportsSpans(t *testing.T) {
	col := newFakeOTLP(t)
	doc := routingPrivacyDoc()
	g, key := bootObservability(t, routingAcceptanceConfig(t, &doc, func(cfg map[string]any, _ string) {
		cfg["otel"] = map[string]any{"endpoint": col.endpoint(), "insecure": true}
	}))
	if rec := routingCall(t, g, key, "hello"); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	flushObservability(t, g)
	if spans := col.spanNames(); len(spans) != 1 {
		t.Fatalf("legacy otel spans = %v", spans)
	}
}

func TestGatewayObservabilityDeniedRequestExportsNoContent(t *testing.T) {
	t.Setenv("OBS_ACCEPTANCE_TOKEN", "Bearer acceptance-collector")
	corp, vault := newFakeOTLP(t), newFakeOTLP(t)
	doc := observabilityDoc(&v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}, Content: &v1alpha1.SignalExport{Exporter: "vault"}})
	g, _ := bootObservability(t, routingAcceptanceConfig(t, &doc, withExporters(corp, vault)))
	key, _, err := g.store.Create(context.Background(), "engineering", []string{"private"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := routingCall(t, g, key, "secret roadmap"); rec.Code != 403 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	flushObservability(t, g)
	if n := len(vault.events("inferplane.content")); n != 0 {
		t.Fatalf("a denied request exported %d content records", n)
	}
	if len(corp.events("inferplane.audit")) == 0 {
		t.Fatal("the denial itself must still reach the governed audit copy")
	}
}
