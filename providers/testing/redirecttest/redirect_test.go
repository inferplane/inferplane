package redirecttest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inferplane/inferplane/providers"
	_ "github.com/inferplane/inferplane/providers/anthropic"
	_ "github.com/inferplane/inferplane/providers/openaicompat"
	_ "github.com/inferplane/inferplane/providers/openairesponses"
)

func TestRedirectNeverLeavesSelectedDestination(t *testing.T) {
	cases := []struct {
		name, kind, protocol, authHeader, authValue string
		settings                                    map[string]string
	}{
		{name: "anthropic-api-key", kind: "anthropic", protocol: "anthropic", authHeader: "X-Api-Key", authValue: "synthetic-test-only"},
		{name: "anthropic-bearer", kind: "anthropic", protocol: "anthropic", authHeader: "Authorization", authValue: "Bearer synthetic-test-only", settings: map[string]string{"auth_header": "bearer"}},
		{name: "openai-compatible", kind: "openai_compatible", protocol: "openai", authHeader: "Authorization", authValue: "Bearer synthetic-test-only"},
		{name: "openai-responses", kind: "openai_responses", protocol: "responses", authHeader: "Authorization", authValue: "Bearer synthetic-test-only"},
	}
	for _, tc := range cases {
		for _, code := range []int{301, 302, 303, 307, 308} {
			for _, mode := range []string{"complete", "stream", "count", "health"} {
				if mode == "count" && tc.kind != "anthropic" ||
					mode == "health" && tc.kind == "openai_responses" {
					continue // Not implemented by these providers.
				}
				for _, injected := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%d/%s/injected=%t", tc.name, code, mode, injected), func(t *testing.T) {
						var sourceCalls, destinationCalls atomic.Int64
						target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							destinationCalls.Add(1)
							_, _ = io.Copy(io.Discard, r.Body)
							w.WriteHeader(http.StatusBadGateway)
						}))
						defer target.Close()
						source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							sourceCalls.Add(1)
							if r.Header.Get(tc.authHeader) != tc.authValue {
								t.Error("configured endpoint did not receive the synthetic gateway credential")
							}
							http.Redirect(w, r, target.URL, code)
						}))
						defer source.Close()

						cfg := providers.Config{
							Type: tc.kind, BaseURL: source.URL, APIKey: "synthetic-test-only", Settings: tc.settings,
						}
						if injected {
							cfg.HTTPClient = source.Client()
							cfg.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
						}
						p, err := providers.New(cfg)
						if err != nil {
							t.Fatal(err)
						}
						raw := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"synthetic"}],"stream":false}`
						if tc.kind == "openai_responses" {
							raw = `{"model":"m","input":"synthetic","stream":false}`
						}
						if mode == "stream" {
							raw = strings.Replace(raw, `"stream":false`, `"stream":true`, 1)
						}
						req := &providers.ProxyRequest{
							IngressProtocol: tc.protocol, Model: "m", Upstream: "m",
							RawBody: []byte(raw), Headers: http.Header{}, Stream: mode == "stream",
						}
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						switch mode {
						case "complete":
							_, _ = p.Complete(ctx, req)
						case "stream":
							seq, err := p.Stream(ctx, req)
							if err == nil {
								for _, streamErr := range seq {
									if streamErr != nil {
										break
									}
								}
							}
						case "count":
							_, _ = p.(providers.TokenCounter).CountTokens(ctx, req)
						case "health":
							_ = p.(providers.HealthChecker).HealthCheck(ctx)
						}
						if got := sourceCalls.Load(); got != 1 {
							t.Fatalf("source_calls=%d, want 1", got)
						}
						if got := destinationCalls.Load(); got != 0 {
							t.Fatalf("source_calls=1 destination_calls=%d, want 0: redirect left selected destination", got)
						}
					})
				}
			}
		}
	}
}
