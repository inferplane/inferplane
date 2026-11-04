package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/internal/tokenest/tokenesttest"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

func estimateMux(window int64, gov *governance.Governor) http.Handler {
	holder := newHolder(map[string]providers.Provider{"p": mockprovider.New("m")},
		map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}, ContextWindow: window}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	return DataMux(router.New(holder), holder, store, nil, gov, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)
}

func estimateDo(mux http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("x-api-key", "dev-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Claude Code reads both the count endpoint and the overflow refusal; they
// must report the same estimate for the same body on both count APIs.
func TestCountAndContextGateAgree(t *testing.T) {
	mux := estimateMux(1000, nil)
	body := tokenesttest.AnthropicBody("m", tokenesttest.ClassNamed("prose").Text(5, 8000))
	overage := regexp.MustCompile(`prompt is too long: (\d+) tokens > 1000 maximum`)
	for _, tc := range []struct{ protocol, generate, count, field string }{
		{"anthropic", "/v1/messages", "/v1/messages/count_tokens", "input_tokens"},
		{"bedrock", "/model/m/invoke", "/model/m/count-tokens", "inputTokens"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			want := tokenest.Estimate(tc.protocol, body)
			rec := estimateDo(mux, tc.generate, body)
			var refusal struct {
				Message string                   `json:"message"`
				Error   struct{ Message string } `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &refusal)
			match := overage.FindStringSubmatch(refusal.Message + refusal.Error.Message)
			if rec.Code != http.StatusBadRequest || match == nil || match[1] != strconv.FormatInt(want, 10) {
				t.Fatalf("gate = %d %s, want estimate %d", rec.Code, rec.Body.String(), want)
			}
			countBody := body
			if tc.protocol == "bedrock" {
				countBody, _ = json.Marshal(map[string]any{"input": map[string]any{"invokeModel": map[string]any{"body": base64.StdEncoding.EncodeToString(body)}}})
			}
			var out map[string]int64
			rec = estimateDo(mux, tc.count, countBody)
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out[tc.field] != want {
				t.Fatalf("count = %d %s, want %d", rec.Code, rec.Body.String(), want)
			}
		})
	}
}

// A Bedrock count answered locally (here the readiness gate refuses, as for a
// shared-authority gateway) estimates the decoded InvokeModel body: the same
// number the context gate reports for it, not the base64 wrapper's estimate.
func TestBedrockLocalCountAgreesWithContextGate(t *testing.T) {
	body := tokenesttest.AnthropicBody("m", tokenesttest.ClassNamed("prose").Text(5, 8000))
	want := tokenest.Estimate("bedrock", body)
	wrapper, _ := json.Marshal(map[string]any{"input": map[string]any{"invokeModel": map[string]any{"body": base64.StdEncoding.EncodeToString(body)}}})
	if tokenest.Estimate("bedrock", wrapper) == want {
		t.Fatal("fixture cannot tell the wrapper's estimate from the body's")
	}
	var refusal struct{ Message string }
	rec := estimateDo(estimateMux(1000, nil), "/model/m/invoke", body)
	_ = json.Unmarshal(rec.Body.Bytes(), &refusal)
	if match := regexp.MustCompile(`prompt is too long: (\d+) tokens`).FindStringSubmatch(refusal.Message); match == nil || match[1] != strconv.FormatInt(want, 10) {
		t.Fatalf("gate = %d %s, want estimate %d", rec.Code, rec.Body.String(), want)
	}
	holder := newHolder(map[string]providers.Provider{"p": mockprovider.New("m")},
		map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}, ContextWindow: 1000}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	unready := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0,
		WithGovernanceGate(func() (bool, string) { return false, "not synced" }))
	var out struct{ InputTokens int64 }
	rec = estimateDo(unready, "/model/m/count-tokens", wrapper)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.InputTokens != want {
		t.Fatalf("local count = %d %s, want %d", rec.Code, rec.Body.String(), want)
	}
}

// The TPM reservation follows the content estimate: prose that bytes/4
// over-counted is now admitted, hex that it under-counted is now refused.
func TestTPMPreCheckUsesContentEstimate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   []byte
		status int
	}{
		{"prose 40 KB (bytes/4 10k, estimate ≈6k)", tokenesttest.AnthropicBody("m", tokenesttest.ClassNamed("prose").Text(6, 40_000)), http.StatusOK},
		{"hex 16 KB (bytes/4 4k, estimate ≈9k)", tokenesttest.AnthropicBody("m", tokenesttest.ClassNamed("hex").Text(6, 16_000)), http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gov := governance.NewGovernor(map[string]governance.TeamPolicy{"t": {TokensPerMinute: 8000}}, limiter.NewMemory(), budget.NewMemory(), nil)
			if rec := estimateDo(estimateMux(0, gov), "/v1/messages", tc.body); rec.Code != tc.status {
				t.Fatalf("status = %d %s, want %d (estimate %d)", rec.Code, rec.Body.String(), tc.status, tokenest.Estimate("anthropic", tc.body))
			}
		})
	}
}
