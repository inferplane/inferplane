package server

import (
	"encoding/json"
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

// The 2026-10-27 path check: a 65 MiB Content-Length got
// {"error":"request body too large"} on every ingress, an envelope no client
// parses, and an undeclared oversized body got a generic 400 on most of them.
func TestOversizedBodyIsProtocolShaped(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("m")}
	holder := newHolder(provs, map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0, WithMaxRequestBytes(64))
	body := `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", 256) + `"}]}`
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", "/model/m/invoke", "/model/m/converse"} {
		for _, declared := range []bool{true, false} {
			t.Run(path+map[bool]string{true: "/declared", false: "/undeclared"}[declared], func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header.Set("x-api-key", "dev-key")
				if !declared {
					req.ContentLength = -1
				}
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				var reply struct {
					Type    string                         `json:"type"`
					Message string                         `json:"message"`
					Error   struct{ Type, Message string } `json:"error"`
				}
				if rec.Code != http.StatusRequestEntityTooLarge || json.Unmarshal(rec.Body.Bytes(), &reply) != nil {
					t.Fatalf("oversized body = %d %s, want a parseable 413", rec.Code, rec.Body.String())
				}
				switch {
				case strings.HasPrefix(path, "/model/"):
					if rec.Header().Get("X-Amzn-ErrorType") != "ValidationException" || reply.Message != "request body too large" {
						t.Fatalf("not AWS-shaped: %s", rec.Body.String())
					}
				case path == "/v1/messages":
					if reply.Type != "error" || reply.Error.Type != "request_too_large" || reply.Error.Message != "request body too large" {
						t.Fatalf("not Anthropic-shaped: %s", rec.Body.String())
					}
				default:
					if reply.Error.Type != "invalid_request_error" || reply.Error.Message != "request body too large" {
						t.Fatalf("not OpenAI-shaped: %s", rec.Body.String())
					}
				}
			})
		}
	}
}
