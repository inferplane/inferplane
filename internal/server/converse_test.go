package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

type nativeMuxProvider struct{ providers.Provider }

func (nativeMuxProvider) SupportsBedrockConverse(string) bool { return true }

func TestConverseMuxRoutesShareAuthenticationReadinessAndBodyLimit(t *testing.T) {
	p := nativeMuxProvider{mockprovider.New("native")}
	holder := newHolder(map[string]providers.Provider{"p": p}, map[string]config.ModelConfig{
		"native": {Targets: []config.Target{{Provider: "p", Model: "native"}}},
	})
	store := stubStore{key: "fixture-key", p: keystore.Principal{AllowedModels: []string{"*"}}}
	for _, operation := range []string{"converse", "converse-stream"} {
		for _, tc := range []struct {
			name      string
			auth      bool
			option    DataMuxOption
			want      int
			errorType string
		}{
			{"auth", false, WithMaxRequestBytes(4096), 401, "UnauthorizedException"},
			{"readiness", true, WithGovernanceGate(func() (bool, string) { return false, "stale" }), 503, "ServiceUnavailableException"},
			{"body limit", true, WithMaxRequestBytes(2), 413, "ValidationException"},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0, tc.option)
				req := httptest.NewRequest(http.MethodPost, "/model/native/"+operation, strings.NewReader(`{"messages":[]}`))
				if tc.auth {
					req.Header.Set("X-Api-Key", "fixture-key")
				}
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code != tc.want || rec.Header().Get("X-Amzn-ErrorType") != tc.errorType || !strings.Contains(rec.Body.String(), `"message":`) {
					t.Fatalf("wrong Bedrock gate response: %d %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
	mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)
	req := httptest.NewRequest("POST", "/model/native/converse", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("X-Api-Key", "fixture-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("Converse still missing: %d %s", rec.Code, rec.Body.String())
	}
}
