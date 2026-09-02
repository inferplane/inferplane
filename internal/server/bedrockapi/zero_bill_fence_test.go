package bedrockapi

// Phase 0a invariant fence — see anthropicapi/zero_bill_fence_test.go; this
// is the same fence on the Bedrock ingress, where the Mantle Complete bug
// (a 200 that billed zero) originally re-opened the ADR-030 class.

import (
	"context"
	"io"
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
)

// unbilledProvider returns a 200 whose body produced no canonical response.
// Name() is "mock" so it passes the servesBedrockIngress filter (test-only
// allowance, same as statusProvider).
type unbilledProvider struct{}

func (unbilledProvider) Name() string               { return "mock" }
func (unbilledProvider) Models() []schema.ModelInfo { return nil }
func (unbilledProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return &providers.ProxyResponse{StatusCode: 200, RawBody: []byte(`certainly-not-json`)}, nil
}
func (unbilledProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, nil
}

func TestInvokeUnaccountable2xxFailsClosed(t *testing.T) {
	provs := map[string]providers.Provider{"p": unbilledProvider{}}
	models := map[string]config.ModelConfig{
		"claude-x": {Targets: []config.Target{{Provider: "p", Model: "anthropic.claude-x-v1:0"}}},
	}
	h := holderFor(provs, models)
	handler := NewInvokeHandler(router.New(h), h, false)

	req := invokeReq("claude-x", `{"anthropic_version":"bedrock-2023-05-31","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req.WithContext(ctx))
	body, _ := io.ReadAll(rec.Result().Body)
	if rec.Code != 502 {
		t.Fatalf("unaccountable 2xx must fail closed as 502, got %d: %s", rec.Code, body)
	}
	if !strings.Contains(string(body), "unbilled") {
		t.Fatalf("error should say why: %s", body)
	}
}
