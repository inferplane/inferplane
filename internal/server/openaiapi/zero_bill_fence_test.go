package openaiapi

// Phase 0a invariant fence — see anthropicapi/zero_bill_fence_test.go; this
// is the same fence on the Chat Completions ingress, where the openai-wire
// verbatim tee would otherwise serve the unaccountable bytes untouched.

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

type unbilledProvider struct{}

func (unbilledProvider) Name() string               { return "openai_compatible" }
func (unbilledProvider) Models() []schema.ModelInfo { return nil }
func (unbilledProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return &providers.ProxyResponse{StatusCode: 200, RawBody: []byte(`certainly-not-json`)}, nil
}
func (unbilledProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, nil
}

func TestChatUnaccountable2xxFailsClosed(t *testing.T) {
	provs := map[string]providers.Provider{"p": unbilledProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}}
	h := NewChatHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 502 {
		t.Fatalf("unaccountable 2xx must fail closed as 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestChatUnaccountable2xxFallsBackToNextTarget(t *testing.T) {
	provs := map[string]providers.Provider{
		"bad":  unbilledProvider{},
		"good": mockprovider.New("m"),
	}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{
		{Provider: "bad", Model: "m"},
		{Provider: "good", Model: "m"},
	}}}
	h := NewChatHandler(router.New(holderFor(provs, models)))
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 200 {
		t.Fatalf("an accountable next target must serve the request, got %d: %s", rec.Code, rec.Body.String())
	}
}
