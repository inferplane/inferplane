package server

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/pkg/schema"
)

// Measured 2026-10-27: CLAUDE_CODE_AUTO_MODE_MODEL set to a nonexistent
// global.anthropic.claude-sonnet-9 was silently served by sonnet-5-5. The
// served model stays the same; the substitution is now visible.
func TestModelFallbackIsVisibleOnEveryIngress(t *testing.T) {
	ingresses := append(append([]policyIngress(nil), policyIngresses...), policyIngress{"responses", "/v1/responses", false})
	for _, ing := range ingresses {
		t.Run(ing.name(), func(t *testing.T) {
			f := routingtest.New(t, func(cfg *config.Config) {
				cfg.Models["claude-sonnet-5-5"] = config.ModelConfig{ContextWindow: 100000, Capabilities: []string{"tools"},
					Targets: []config.Target{{Provider: "public", Model: "up"}}}
			})
			in, out := int64(5), int64(1)
			f.Public.Usage = &schema.Usage{InputTokens: &in, OutputTokens: &out} // Responses requires usage
			m := metrics.New()
			f.Router.SetMetrics(m)
			aud, buf := policyAudit(t)
			body := strings.Replace(routingtest.Body(ing.protocol, "clean", ing.stream), `"premium"`, `"claude-sonnet-9"`, 1)
			path := strings.Replace(ing.path, "premium", "claude-sonnet-9", 1)
			if ing.protocol == "responses" {
				body = `{"model":"claude-sonnet-9","stream":false,"store":false,"max_output_tokens":16,"input":"hello"}`
			}
			rec := policyDo(policyMux(f, aud, nil, m, nil), path, body, true)
			aud.Close()
			if rec.Code != 200 || len(f.Public.Calls) != 1 || f.Public.Calls[0].Model != "claude-sonnet-5-5" {
				t.Fatalf("fallback changed the served model: %d calls=%+v body=%s", rec.Code, f.Public.Calls, rec.Body.String())
			}
			if got := rec.Header().Get("x-inferplane-model-fallback"); got != "claude-sonnet-5-5" {
				t.Fatalf("fallback header = %q", got)
			}
			var completed *audit.Record
			scanner := bufio.NewScanner(buf)
			scanner.Buffer(make([]byte, 1<<20), 1<<20)
			for scanner.Scan() {
				var rec audit.Record
				if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
					t.Fatal(err)
				}
				if rec.Event == "request_completed" {
					completed = &rec
				}
			}
			if completed == nil || completed.Request.Routing == nil || completed.Request.Routing.Reason != "model_fallback" ||
				completed.Request.Routing.ModelFallbackFrom != "claude-sonnet-9" || completed.Request.Routing.ModelFallbackSource != "family" {
				t.Fatalf("audit lacks the fallback: %+v", completed)
			}
			families, err := m.Registry().Gather()
			if err != nil {
				t.Fatal(err)
			}
			counted := 0.0
			for _, family := range families {
				if family.GetName() == "inferplane_model_fallback_total" {
					for _, metric := range family.GetMetric() {
						counted += metric.GetCounter().GetValue()
					}
				}
			}
			if counted != 1 {
				t.Fatalf("inferplane_model_fallback_total = %v, want 1", counted)
			}
		})
	}
}
