// Offline browser fixture: real admin/auth/routing/governance/settlement code,
// deterministic providers. Never uses AWS credentials or external endpoints.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/server"
	"github.com/inferplane/inferplane/internal/server/configapi"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

type fixtureProvider struct{}

func (fixtureProvider) Name() string                        { return "mock" }
func (fixtureProvider) Models() []schema.ModelInfo          { return nil }
func (fixtureProvider) SupportsIngress(string) bool         { return true }
func (fixtureProvider) SupportsBedrockConverse(string) bool { return true }
func (fixtureProvider) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	delay := 35 * time.Millisecond
	if req.Upstream == "fixture-slow" {
		delay = 10 * time.Second
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(delay):
	}
	in, out := int64(20), int64(4)
	stop, text := "end_turn", "Local fixture response, not a model quality result."
	if req.Upstream == "fixture-refused" {
		stop, text = "guardrail_intervened", ""
	}
	u := &schema.Usage{InputTokens: &in, OutputTokens: &out, AccountingUncertain: req.Upstream == "fixture-uncertain"}
	var raw []byte
	if req.IngressProtocol == "bedrock-converse" {
		raw, _ = json.Marshal(map[string]any{"output": map[string]any{"message": map[string]any{"role": "assistant", "content": []map[string]string{{"text": text}}}},
			"stopReason": stop, "usage": map[string]int64{"inputTokens": in, "outputTokens": out}})
	} else {
		raw, _ = json.Marshal(&schema.ChatResponse{Type: "message", Role: "assistant",
			Content: []schema.ContentBlock{{Type: "text", Text: &text}}, StopReason: &stop, Usage: u})
	}
	return &providers.ProxyResponse{StatusCode: 200, RawBody: raw, Parsed: &schema.ChatResponse{Usage: u, StopReason: &stop}}, nil
}
func (fixtureProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, errors.New("fixture streams unused")
}

func main() {
	if len(os.Args) != 2 {
		panic("provide a private output directory")
	}
	dir := os.Args[1]
	must(os.MkdirAll(dir, 0700))
	store, err := keystore.OpenSQLite(filepath.Join(dir, "keys.db"))
	must(err)
	defer store.Close()
	key, _, err := store.Create(context.Background(), "fixture", []string{"*"})
	must(err)
	must(os.WriteFile(filepath.Join(dir, "virtual-key"), []byte(key), 0600))
	sink, err := audit.NewFileSink(filepath.Join(dir, "audit.jsonl"), true)
	must(err)
	aud, err := audit.NewWriter("evaluation-fixture", filepath.Join(dir, "audit.wal"), []audit.Sink{sink})
	must(err)
	defer aud.Close()
	models := map[string]config.ModelConfig{}
	rates := map[pricing.Key]pricing.Rate{}
	for _, name := range []string{"fixture-ok", "fixture-refused", "fixture-uncertain", "fixture-unpriced", "fixture-slow"} {
		models[name] = config.ModelConfig{ContextWindow: 8192, Targets: []config.Target{{Provider: "local", Model: name}}}
		if name != "fixture-unpriced" {
			rates[pricing.Key{Provider: "local", Model: name}] = pricing.Rate{InputPerMTok: 1000000, OutputPerMTok: 2000000}
		}
	}
	holder := &live.Holder{}
	holder.Swap(live.NewState(map[string]providers.Provider{"local": fixtureProvider{}}, models, pricing.New(pricing.OnMissingAllow, rates), map[string]string{"local": "fixture"}))
	m := metrics.New()
	gov := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), m)
	data := server.DataMux(router.New(holder), holder, store, aud, gov, m, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)
	topology := func() configapi.View {
		return configapi.ViewFrom(map[string]config.ProviderConfig{"local": {Type: "mock"}}, models)
	}
	admin := server.AdminMux(store, []string{"fixture-admin"}, nil, adminauth.MappingConfig{}, topology,
		[]string{filepath.Join(dir, "audit.jsonl")}, aud, nil, nil, m, nil, nil,
		func() configapi.Capabilities {
			return configapi.Capabilities{AnalyticsIndex: "off", TeamsRecords: true}
		},
		nil, store, nil, nil, nil, nil, nil, nil)
	mux := server.WithEvaluations(admin, data, []string{"fixture-admin"}, nil, adminauth.MappingConfig{}, aud)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	must(os.WriteFile(filepath.Join(dir, "origin"), []byte("http://"+listener.Addr().String()), 0600))
	fmt.Println("offline evaluation fixture ready")
	must(http.Serve(listener, mux))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
