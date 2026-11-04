package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/server/routingtest"
)

func responsesBody(t *testing.T, stream bool, input string, tools bool) string {
	t.Helper()
	body := map[string]any{"model": "premium", "stream": stream, "store": false, "max_output_tokens": 16, "input": input}
	if tools {
		body["tools"] = []any{map[string]any{"type": "function", "name": "read", "strict": false, "parameters": map[string]any{"type": "object"}}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Measured 2026-10-27: Codex on the stateless bridge got 403 "no
// policy-permitted compatible Responses target" for an over-window request
// and retried it unchanged five times. A size-only refusal is now the same
// recoverable 400 the native preflight returns; other refusals keep 403.
func TestResponsesBridgeOverflowIsRecoverable(t *testing.T) {
	big := strings.Repeat("word ", 150_000) // ≥150k tokens by any estimate; window 100k
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[stream], func(t *testing.T) {
			f := routingtest.New(t, nil)
			rec := policyDo(policyMux(f, nil, nil, nil, nil), "/v1/responses", responsesBody(t, stream, big, false), true)
			var reply struct {
				Error struct{ Type, Code, Message string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
				t.Fatalf("unreadable reply %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusBadRequest || reply.Error.Code != "context_length_exceeded" ||
				reply.Error.Type != "invalid_request_error" || len(f.Public.Calls) != 0 {
				t.Fatalf("over-window bridge request = %d %s, calls=%d; want recoverable 400 without an attempt", rec.Code, rec.Body.String(), len(f.Public.Calls))
			}

			// Not a size problem: the only target lacks the declared tools capability.
			f = routingtest.New(t, func(cfg *config.Config) {
				m := cfg.Models["premium"]
				m.Capabilities = nil
				cfg.Models["premium"] = m
			})
			rec = policyDo(policyMux(f, nil, nil, nil, nil), "/v1/responses", responsesBody(t, stream, "hello", true), true)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "no policy-permitted compatible Responses target") {
				t.Fatalf("non-context refusal changed: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
