package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/sensitivity"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/pkg/schema"
)

// A long tool result can exceed the inspector's conservative byte ceiling
// while fitting the explicit model's existing ingress context estimate.
func TestResponsesRequestedModelAcceptsLongToolHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, model := range []string{"premium", "premium-alias"} {
			t.Run(model+map[bool]string{false: "/complete", true: "/stream"}[stream], func(t *testing.T) {
				f := routingtest.New(t, func(cfg *config.Config) {
					m := cfg.Models["premium"]
					m.Aliases = []string{"premium-alias"}
					cfg.Models["premium"] = m
				})
				in, out := int64(40000), int64(2)
				f.Public.Usage = &schema.Usage{InputTokens: &in, OutputTokens: &out}
				body, err := json.Marshal(map[string]any{
					"model": model, "stream": stream, "store": false,
					"max_output_tokens": 128,
					"reasoning":         map[string]any{"effort": "none"},
					"tools": []any{map[string]any{
						"type": "function", "name": "read_document", "strict": false,
						"parameters": map[string]any{"type": "object"},
					}},
					"input": []any{
						map[string]any{"role": "user", "content": "Read the routing documentation."},
						map[string]any{"type": "function_call", "call_id": "call_read", "name": "read_document", "arguments": "{}"},
						map[string]any{
							"type": "function_call_output", "call_id": "call_read",
							"output": strings.Repeat("Routing reference text.\n", 8192),
						},
						map[string]any{"role": "user", "content": "Summarize the document."},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				inspection, err := sensitivity.NewInspector().Inspect(context.Background(), "responses", body)
				limit := f.Config.Models["premium"].ContextWindow
				if err != nil || !inspection.Complete || inspection.InputTokens <= limit ||
					int64(len(body)/4)+inspection.OutputTokens >= limit {
					t.Fatalf("fixture must separate context estimate from inspection ceiling: inspection=%+v bytes=%d err=%v", inspection, len(body), err)
				}
				rec := policyDo(policyMux(f, nil, nil, nil, nil), "/v1/responses", string(body), true)
				if rec.Code != http.StatusOK {
					t.Fatalf("long portable history rejected: status=%d body=%s", rec.Code, rec.Body.String())
				}
				if len(f.Public.Calls) != 1 || len(f.Private.Calls) != 0 {
					t.Fatalf("unexpected attempts: public=%d private=%d", len(f.Public.Calls), len(f.Private.Calls))
				}
				if got := f.Public.Calls[0].Body; got != string(body) {
					t.Fatal("context admission changed the request body")
				}
				if stream {
					if !strings.Contains(rec.Body.String(), "event: response.completed") {
						t.Fatalf("stream did not complete: %s", rec.Body.String())
					}
				} else {
					var response struct {
						Status string `json:"status"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Status != "completed" {
						t.Fatalf("response did not complete: %s", rec.Body.String())
					}
				}
			})
		}
	}
}
