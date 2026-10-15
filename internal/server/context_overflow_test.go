package server

import (
	"context"
	"encoding/json"
	"iter"
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
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

type contextOverflowProbe struct {
	providers.Provider
	calls int
	last  string
}

func (p *contextOverflowProbe) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	p.calls++
	p.last = string(req.RawBody)
	return p.Provider.Complete(ctx, req)
}

func (p *contextOverflowProbe) Stream(ctx context.Context, req *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	p.calls++
	p.last = string(req.RawBody)
	return p.Provider.Stream(ctx, req)
}

// A screenshot's base64 can be large even when its visual content is small.
// Keep the original bytes and all governance checks, but do not reject it as
// thousands of text tokens before an upstream can tokenize the image.
func TestInlineImageContextPreflightPreservesAdmissionAndWire(t *testing.T) {
	data := strings.Repeat("AAAA", 4096)
	image := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}`
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/model/m/invoke", "/model/m/invoke-with-response-stream"} {
		for _, mode := range []string{"ordinary", "stream", "text-overflow", "tpm-block"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				probe := &contextOverflowProbe{Provider: mockprovider.New("m")}
				holder := newHolder(map[string]providers.Provider{"p": probe}, map[string]config.ModelConfig{
					"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}, ContextWindow: 1000, Capabilities: []string{"vision"}},
				})
				var gov *governance.Governor
				if mode == "tpm-block" {
					gov = governance.NewGovernor(map[string]governance.TeamPolicy{"team": {TokensPerMinute: 1000}}, limiter.NewMemory(), budget.NewMemory(), nil)
				}
				store := stubStore{key: "fixture-key", p: keystore.Principal{Team: "team", AllowedModels: []string{"*"}}}
				mux := DataMux(router.New(holder), holder, store, nil, gov, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)
				text := "Summarize the screenshot."
				if mode == "text-overflow" {
					text = strings.Repeat("word ", 1000)
				}
				content := `{"type":"tool_result","tool_use_id":"call","content":[` + image + `,{"type":"text","text":"` + text + `"}]}`
				if path == "/v1/chat/completions" {
					content = `{"type":"image_url","image_url":{"url":"data:image/png;base64,` + data + `"}},{"type":"text","text":"` + text + `"}`
				}
				body := `{"model":"m","max_tokens":16,"stream":` + strconv.FormatBool(mode == "stream") + `,"messages":[{"role":"user","content":[` + content + `]}]}`
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header.Set("x-api-key", "fixture-key")
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				wantStatus, wantCalls := http.StatusOK, 1
				if mode == "text-overflow" {
					wantStatus, wantCalls = http.StatusBadRequest, 0
				} else if mode == "tpm-block" {
					wantStatus, wantCalls = http.StatusTooManyRequests, 0
				}
				if rec.Code != wantStatus || probe.calls != wantCalls {
					t.Fatalf("status=%d calls=%d; want status=%d calls=%d; response=%s", rec.Code, probe.calls, wantStatus, wantCalls, rec.Body.String())
				}
				if wantCalls > 0 && probe.last != body {
					t.Fatal("context preflight altered image/history bytes")
				}
			})
		}
	}
}

// Claude Code 2.1.292 recognizes a 400 containing "prompt is too long" and
// extracts the overage using this regex. A generic "request is ~N tokens"
// instead enters last-resort model fallback, replaying the oversized history.
// Pin the client contract at the actual ingress boundary, not a formatter.
func TestContextOverflowSupportsClientCompaction(t *testing.T) {
	const window = 100
	clientCounts := regexp.MustCompile(`(?i)prompt is too long[^0-9]*(\d+)\s*tokens?\s*>\s*(\d+)`)
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/model/m/invoke", "/model/m/invoke-with-response-stream"} {
		for _, stream := range []bool{false, true} {
			t.Run(path+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				probe := &contextOverflowProbe{Provider: mockprovider.New("m")}
				holder := newHolder(map[string]providers.Provider{"p": probe}, map[string]config.ModelConfig{
					"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}, ContextWindow: window},
				})
				store := stubStore{key: "fixture-key", p: keystore.Principal{Team: "team", AllowedModels: []string{"*"}}}
				mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)
				body := `{"model":"m","max_tokens":16,"stream":` + strconv.FormatBool(stream) + `,"messages":[{"role":"user","content":"` + strings.Repeat("word ", 400) + `"}]}`
				do := func(path string, authenticated bool) *httptest.ResponseRecorder {
					req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					if authenticated {
						req.Header.Set("x-api-key", "fixture-key")
					}
					rec := httptest.NewRecorder()
					mux.ServeHTTP(rec, req)
					return rec
				}
				rec := do(path, true)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("overflow = %d %s", rec.Code, rec.Body.String())
				}
				var reply struct {
					Message string `json:"message"`
					Error   struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
					t.Fatal(err)
				}
				message := reply.Error.Message
				if strings.HasPrefix(path, "/model/") {
					message = reply.Message
					if rec.Header().Get("X-Amzn-ErrorType") != "ValidationException" || reply.Error.Type != "" {
						t.Fatalf("lost Bedrock error envelope: %s", rec.Body.String())
					}
				} else if reply.Error.Type != "invalid_request_error" {
					t.Fatalf("lost ingress error type: %s", rec.Body.String())
				}
				counts := clientCounts.FindStringSubmatch(message)
				if len(counts) != 3 || counts[1] != strconv.Itoa(len(body)/4) || counts[2] != strconv.Itoa(window) {
					t.Fatalf("client cannot recognize overflow and size its compaction: %q", message)
				}
				if !strings.Contains(message, "estimate") || !strings.Contains(strings.ToLower(message), "compact") {
					t.Fatalf("error must distinguish estimate from measured usage and explain recovery: %q", message)
				}
				// The wording change must not move the gate after provider work,
				// leak model/window details before auth, or break count_tokens.
				if rec := do(path, false); rec.Code != http.StatusUnauthorized || clientCounts.Match(rec.Body.Bytes()) {
					t.Fatalf("unauthenticated overflow exposed context details: %d %s", rec.Code, rec.Body.String())
				}
				for _, countPath := range []string{"/v1/messages/count_tokens", "/model/m/count-tokens"} {
					if rec := do(countPath, true); rec.Code != http.StatusOK {
						t.Fatalf("count refused oversized context: %d %s", rec.Code, rec.Body.String())
					}
				}
				if probe.calls != 0 {
					t.Fatalf("overflow reached provider %d times", probe.calls)
				}
			})
		}
	}
}
