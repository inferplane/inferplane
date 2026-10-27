package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/testutil/inspectionfixture"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

type gatewayInspectionProvider struct {
	providers.Provider
	wire  string
	calls atomic.Int64
}

func (p *gatewayInspectionProvider) Name() string {
	if p.wire == "responses" {
		return "openai_responses"
	}
	return "mock"
}
func (p *gatewayInspectionProvider) SupportsIngress(string) bool         { return true }
func (p *gatewayInspectionProvider) SupportsBedrockConverse(string) bool { return true }
func (p *gatewayInspectionProvider) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	p.calls.Add(1)
	resp, err := p.Provider.Complete(ctx, req)
	if p.wire == "responses" && err == nil {
		resp.RawBody = []byte(`{"id":"resp_fixture","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`)
	}
	return resp, err
}

type gatewayInspectionClassifier struct{}

func (gatewayInspectionClassifier) Windows(s []string) []string { return s }
func (gatewayInspectionClassifier) Score(_ context.Context, s []string) (classifier.Scores, error) {
	v := make([]classifier.Verdict, len(s))
	for i := range v {
		v[i] = classifier.Clear
	}
	return classifier.Scores{Verdicts: v, Sent: len(v)}, nil
}

func init() {
	for _, protocol := range inspectionfixture.Protocols {
		providers.Register("gateway-inspection-"+protocol, func(providers.Config) (providers.Provider, error) {
			return &gatewayInspectionProvider{Provider: mockprovider.New("fixture"), wire: protocol}, nil
		})
	}
	classifier.Register("gateway-inspection-clear", func(classifier.Config) (classifier.Classifier, error) {
		return gatewayInspectionClassifier{}, nil
	})
}

func inspectionBenchmarkGateway(b *testing.B, protocol, mode string) (*gateway, string, *gatewayInspectionProvider) {
	b.Helper()
	dir := b.TempDir()
	rate := map[string]any{"free": true}
	cfg := map[string]any{
		"server":    map[string]any{"listen": "127.0.0.1:0", "admin_listen": "127.0.0.1:0"},
		"key_store": map[string]any{"type": "sqlite", "path": filepath.Join(dir, "keys.db")},
		"audit":     map[string]any{"buffer": map[string]any{"path": filepath.Join(dir, "audit.wal")}},
		"analytics": map[string]any{"disabled": true},
		"teams":     map[string]any{"team": map[string]any{"allowed_models": []string{"*"}}},
		"providers": map[string]any{"fixture": map[string]any{"type": "gateway-inspection-" + protocol, "base_url": "https://fixture.invalid", "data_boundary": "internal"}},
		"models": map[string]any{
			"premium": map[string]any{"context_window": 64 << 20, "capabilities": []string{"tools"}, "targets": []map[string]any{{"provider": "fixture", "model": "fixture"}}},
		},
		"pricing": map[string]any{"on_missing": "block", "overrides": map[string]any{"fixture": map[string]any{"fixture": rate}}},
	}
	if mode == "classifier" {
		cfg["pii_classification"] = map[string]any{
			"max_input_bytes": 16 << 20, "cache": map[string]any{"max_entries": 0},
			"classifiers": []map[string]any{{"name": "fixture", "type": "gateway-inspection-clear", "mode": "Enforce", "endpoint": "http://127.0.0.1:1", "data_boundary": "internal"}},
		}
	}
	if mode == "legacy" {
		cfg["plugins"] = []map[string]any{{"name": "pii-mask"}}
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		b.Fatal(err)
	}
	g, err := newGateway(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		g.dataLn.Close()
		g.adminLn.Close()
		g.aud.Close()
		g.store.Close()
	})
	key, _, err := g.store.Create(context.Background(), "team", []string{"*"})
	if err != nil {
		b.Fatal(err)
	}
	if mode != "none" && mode != "legacy" {
		doc := &policy.Policy{Name: "benchmark", Subject: policy.Subject{Team: "team"}, Rules: []policy.Rule{{Name: mode}}}
		rule := &doc.Rules[0]
		if mode == "shadow" || mode == "enforce" {
			contextMode := v1alpha1.Shadow
			if mode == "enforce" {
				contextMode = v1alpha1.Enforce
			}
			rule.Routing = &policy.Routing{Context: &policy.Context{Mode: contextMode, FromModels: []string{"premium"}, SimpleModel: "premium", ComplexModel: "premium", MaxSimpleInputTokens: 1000}}
		} else if mode == "strict" {
			g.router.SetBudgetConstraintGate(func(keystore.Principal) map[string]string { return map[string]string{"premium": "premium"} })
		} else {
			action := v1alpha1.InternalOnly
			if mode == "block" {
				action = v1alpha1.Block
			}
			if mode == "mask" {
				action = v1alpha1.Mask
			}
			rule.SensitiveData = &policy.SensitiveData{OnDetected: action, OnUninspectable: v1alpha1.Block, InternalModels: []string{"premium"}}
		}
		if mode != "strict" {
			g.router.SetRoutingPolicyLookup(func(string, string) ([]*policy.Policy, error) { return []*policy.Policy{doc}, nil })
		}
	}
	state := g.holder.Load()
	provider, ok := state.Provider("fixture")
	if !ok {
		b.Fatal("benchmark provider missing")
	}
	p := provider.(*gatewayInspectionProvider)
	return g, key, p
}

func BenchmarkGatewayInspection(b *testing.B) {
	for _, protocol := range inspectionfixture.Protocols {
		for _, mode := range []string{"none", "shadow", "enforce", "strict", "internal", "block", "mask", "legacy", "classifier"} {
			// The legacy OpenAI Chat filter is deliberately unsupported.
			if mode == "legacy" && protocol != "responses" && protocol != "anthropic" {
				continue
			}
			sizes := inspectionfixture.Sizes
			if mode != "none" {
				sizes = []int{200 << 10, 1 << 20}
			}
			for _, size := range sizes {
				concurrency := []int{1}
				if protocol == "responses" && (mode == "none" || mode == "shadow") {
					concurrency = inspectionfixture.Concurrency
				}
				for _, c := range concurrency {
					b.Run(fmt.Sprintf("%s/%s/bytes=%d/c=%d", protocol, mode, size, c), func(b *testing.B) {
						g, key, provider := inspectionBenchmarkGateway(b, protocol, mode)
						raw := inspectionfixture.Request(protocol, size)
						if mode == "block" || mode == "mask" || mode == "legacy" {
							raw = []byte(strings.Replace(string(raw), "synthetic fixture text ", "person@example.test    ", 1))
						}
						path := map[string]string{"anthropic": "/v1/messages", "openai": "/v1/chat/completions", "responses": "/v1/responses", "bedrock": "/model/premium/invoke", "bedrock-converse": "/model/premium/converse"}[protocol]
						want := http.StatusOK
						// The existing classifier segment API rejects the native
						// alias; retain that fail-closed refusal in this comparison.
						refused := mode == "block" || (mode == "classifier" && protocol == "bedrock-converse")
						if refused {
							want = http.StatusForbidden
						}
						// Warm up and verify actual admission before measuring.
						call := func() error {
							req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
							req.Header.Set("Authorization", "Bearer "+key)
							rec := httptest.NewRecorder()
							g.dataSrv.Handler.ServeHTTP(rec, req)
							if rec.Code != want {
								return fmt.Errorf("fixture %s/%s returned %d instead of %d: %s", protocol, mode, rec.Code, want, rec.Body.String())
							}
							return nil
						}
						if err := call(); err != nil {
							b.Fatal(err)
						}
						before := provider.calls.Load()
						inspectionfixture.Run(b, len(raw), c, call)
						calls := provider.calls.Load() - before
						b.ReportMetric(float64(calls)/float64(b.N), "dispatches/op")
						if (refused && calls != 0) || (!refused && calls != int64(b.N)) {
							b.Fatal("synthetic dispatch count does not match admitted requests")
						}
					})
				}
			}
		}
	}
}
