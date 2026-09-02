package anthropicapi

// Phase 0a invariant fence (enterprise-strategy: a settled cost is
// mandatory for every 2xx): a provider returning a success the gateway
// cannot account (Parsed == nil) must never be served unbilled — the
// ADR-030 zero-cost class, previously re-opened by the Mantle Complete
// path and fixed only provider-side. These tests pin the INGRESS fence.

import (
	"context"
	"iter"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

// unbilledProvider returns a 200 whose body produced no canonical response —
// what a buggy future egress path (re-parsed JSON that failed) looks like.
type unbilledProvider struct{}

func (unbilledProvider) Name() string               { return "unbilled" }
func (unbilledProvider) Models() []schema.ModelInfo { return nil }
func (unbilledProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return &providers.ProxyResponse{StatusCode: 200, RawBody: []byte(`certainly-not-json`)}, nil
}
func (unbilledProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, nil
}

func TestMessagesUnaccountable2xxFailsClosed(t *testing.T) {
	provs := map[string]providers.Provider{"p": unbilledProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 502 {
		t.Fatalf("unaccountable 2xx must fail closed as 502, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unbilled") {
		t.Fatalf("error should say why: %s", rec.Body.String())
	}
}

func TestMessagesUnaccountable2xxFallsBackToNextTarget(t *testing.T) {
	provs := map[string]providers.Provider{
		"bad":  unbilledProvider{},
		"good": mockprovider.New("m"),
	}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{
		{Provider: "bad", Model: "m"},
		{Provider: "good", Model: "m"},
	}}}
	h := NewMessagesHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 200 {
		t.Fatalf("an accountable next target must serve the request, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "msg_mock") {
		t.Fatalf("response should come from the good target: %s", rec.Body.String())
	}
}
