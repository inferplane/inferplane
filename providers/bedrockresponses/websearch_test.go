package bedrockresponses

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/inferplane/inferplane/providers"
)

// Native hosted tools must remain native: search controls, search-call items,
// citations and SSE events are not converted into local function calls.
func TestWebSearchNativeRoundTrip(t *testing.T) {
	stubCredentials(t, aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return testCredentials, nil
	}))
	const result = `{"id":"r","status":"completed","output":[{"type":"web_search_call","id":"ws","status":"completed","action":{"type":"search","query":"AWS docs"}},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"AWS docs","annotations":[{"type":"url_citation","title":"AWS docs","url":"https://docs.aws.amazon.com/","start_index":0,"end_index":8}]}]}],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":8},"output_tokens":2}}`
	const events = "event: response.web_search_call.in_progress\ndata: {\"type\":\"response.web_search_call.in_progress\",\"output_index\":0}\n\n" +
		"event: response.output_text.annotation.added\ndata: {\"type\":\"response.output_text.annotation.added\",\"annotation\":{\"type\":\"url_citation\",\"url\":\"https://docs.aws.amazon.com/\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + result + "}\n\n"
	for _, model := range []string{"openai.gpt-5.6-sol", "openai.gpt-5.6-terra", "openai.gpt-5.6-luna", "openai.gpt-6-astra", "openai.gpt-6-sol", "openai.gpt-6-luna"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", model, stream), func(t *testing.T) {
				raw := fmt.Sprintf(` { "model" : "public", "input":"Find AWS docs", "tools":[{"type":"web_search","external_web_access":false,"search_context_size":"low"}], "tool_choice":"required", "store":false, "stream":%t } `, stream)
				want := strings.Replace(raw, `"public"`, fmt.Sprintf("%q", model), 1)
				calls := 0
				p := newTestProvider(t, providers.Config{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					body, err := io.ReadAll(r.Body)
					r.Body.Close()
					if err != nil || string(body) != want || r.URL.Path != "/openai/v1/responses" {
						t.Fatal("search controls or native endpoint changed")
					}
					assertSignature(t, r, want, "us-east-1", testCredentials)
					if stream {
						return response(http.StatusOK, events), nil
					}
					return response(http.StatusOK, result), nil
				})}})
				req := &providers.ProxyRequest{IngressProtocol: "responses", Model: "public", Upstream: model, RawBody: []byte(raw), Stream: stream}
				if stream {
					seq, err := p.Stream(context.Background(), req)
					if err != nil {
						t.Fatal(err)
					}
					var got strings.Builder
					for ev, err := range seq {
						if err != nil {
							t.Fatal(err)
						}
						got.Write(ev.Raw)
					}
					if got.String() != events {
						t.Fatal("search events/citations were not preserved")
					}
				} else {
					res, err := p.Complete(context.Background(), req)
					if err != nil {
						t.Fatal(err)
					}
					if res.StatusCode != 200 || string(res.RawBody) != result || res.Parsed == nil ||
						*res.Parsed.Usage.InputTokens != 2 || *res.Parsed.Usage.CacheReadInputTokens != 8 || *res.Parsed.Usage.OutputTokens != 2 {
						t.Fatal("search output/citations or observed token usage lost")
					}
				}
				if calls != 1 || string(req.RawBody) != raw {
					t.Fatal("unexpected retry or caller mutation")
				}
			})
		}
	}
}
