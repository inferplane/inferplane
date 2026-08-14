package anthropicapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
)

// A key must not trigger a real upstream CountTokens call for a model outside
// its allow-list: count_tokens must never send content to a provider the
// team isn't allowed to reach. Mirrors bedrockapi's
// TestCountTokensDisallowedModelSkipsUpstream — this ingress had the same
// region/mask guards but was missing the model allow-list check.
func TestCountTokensDisallowedModelSkipsUpstream(t *testing.T) {
	tc := &tcProvider{}
	h := NewCountTokensHandler(tokenCounterRouter(tc))
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(principal.With(req.Context(),
		keystore.Principal{Team: "t", AllowedModels: []string{"some-other-model"}}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "input_tokens") {
		t.Fatalf("count_tokens must always return 200: %d %s", rec.Code, rec.Body.String())
	}
	if tc.called {
		t.Fatal("upstream CountTokens must not be called for a model outside the key's allow-list")
	}
}

// The allowed-key path is unaffected — the real upstream counter is still used.
func TestCountTokensAllowedModelUsesUpstream(t *testing.T) {
	tc := &tcProvider{}
	h := NewCountTokensHandler(tokenCounterRouter(tc))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, countReq("t", `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	if rec.Code != 200 {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !tc.called {
		t.Fatal("upstream CountTokens should have been called for an allowed model")
	}
}
