package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
)

type recoveryTransport func(*http.Request) (*http.Response, error)

func (f recoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Socket-free end-to-end regression: actual native provider, routing and ingress.
// The failing upstream's safe recovery code must reach the client unchanged;
// a 400 must neither be retried nor expose arbitrary upstream error strings.
func TestResponsesNativeRecoveryCodeSurvivesGateway(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, code := range []string{"context_length_exceeded", "invalid_encrypted_content", "private-value"} {
			t.Run(code+map[bool]string{true: "/stream", false: "/complete"}[stream], func(t *testing.T) {
				calls := 0
				var original []byte
				client := &http.Client{Transport: recoveryTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					if string(raw) != string(original) {
						t.Fatal("native request bytes changed")
					}
					if !strings.Contains(string(raw), `"encrypted_content":"opaque-history"`) {
						t.Fatal("native history lost")
					}
					return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"` + code + `","message":"private-value","param":"private-value"}}`))}, nil
				})}
				p, err := providers.New(providers.Config{Type: "openai_responses", BaseURL: "https://example.invalid/v1", HTTPClient: client})
				if err != nil {
					t.Fatal(err)
				}
				holder := newHolder(map[string]providers.Provider{"p": p}, map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}, ContextWindow: 1000000}})
				store := stubStore{key: "fixture-key", p: keystore.Principal{Team: "team", AllowedModels: []string{"*"}}}
				aud, auditBuf := policyAudit(t)
				closed := false
				defer func() {
					if !closed {
						_ = aud.Close()
					}
				}()
				mux := DataMux(router.New(holder), holder, store, aud, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)
				raw, _ := json.Marshal(map[string]any{"model": "m", "stream": stream, "input": []any{map[string]any{"type": "reasoning", "encrypted_content": "opaque-history"}, map[string]any{"role": "user", "content": strings.Repeat("word ", 370000)}}})
				original = raw
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(raw)))
				req.Header.Set("Authorization", "Bearer fixture-key")
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code != 400 || calls != 1 {
					t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls, rec.Body.String())
				}
				want := code
				if code == "private-value" {
					want = ""
				}
				closed = true
				if err := aud.Close(); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(auditBuf.String(), "private-value") {
					t.Fatal("upstream text entered audit")
				}
				if want != "" && !strings.Contains(auditBuf.String(), `"error":"`+want+`"`) {
					t.Fatal("audit lost safe error code")
				}
				if providers.ResponseErrorCode(rec.Body.Bytes()) != want || strings.Contains(rec.Body.String(), "private-value") {
					t.Fatalf("unsafe/lost code: %s", rec.Body.String())
				}
			})
		}
	}
}

func TestResponsesPreflightExposesContextRecoveryCode(t *testing.T) {
	for _, stream := range []bool{false, true} {
		calls := 0
		client := &http.Client{Transport: recoveryTransport(func(*http.Request) (*http.Response, error) {
			calls++
			t.Fatal("context overflow reached upstream")
			return nil, nil
		})}
		p, err := providers.New(providers.Config{Type: "openai_responses", BaseURL: "https://example.invalid/v1", HTTPClient: client})
		if err != nil {
			t.Fatal(err)
		}
		holder := newHolder(map[string]providers.Provider{"p": p}, map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}, ContextWindow: 100}})
		store := stubStore{key: "fixture-key", p: keystore.Principal{Team: "team", AllowedModels: []string{"*"}}}
		mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)
		body, _ := json.Marshal(map[string]any{"model": "m", "stream": stream, "input": strings.Repeat("word ", 400)})
		for _, authenticated := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
			if authenticated {
				req.Header.Set("Authorization", "Bearer fixture-key")
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if authenticated {
				if rec.Code != 400 || providers.ResponseErrorCode(rec.Body.Bytes()) != "context_length_exceeded" {
					t.Fatalf("missing context recovery code: %d %s", rec.Code, rec.Body.String())
				}
			} else if rec.Code != 401 || providers.ResponseErrorCode(rec.Body.Bytes()) != "" {
				t.Fatal("context exposed before auth")
			}
		}
		if calls != 0 {
			t.Fatal("overflow called upstream")
		}
	}
}
