package openairesponses

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferplane/inferplane/providers"
)

func TestCompleteRetainsNativeIncompleteOutput(t *testing.T) {
	for _, output := range []string{
		`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial answer","annotations":[]}]}]`,
		`[{"type":"function_call","call_id":"call","name":"read","arguments":"{\"path\":"}]`,
	} {
		t.Run(output, func(t *testing.T) {
			raw := `{"id":"resp","model":"upstream","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":` + output + `,"usage":{"input_tokens":10,"output_tokens":4}}`
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, raw)
			}))
			defer upstream.Close()
			p, err := factory(providers.Config{BaseURL: upstream.URL})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := p.Complete(context.Background(), &providers.ProxyRequest{
				IngressProtocol: "responses", Model: "public", Upstream: "upstream",
				RawBody: []byte(`{"model":"public","input":"hello","max_output_tokens":4}`),
			})
			if err != nil || resp == nil || resp.StatusCode != 200 || string(resp.RawBody) != raw {
				t.Fatalf("native terminal incomplete output changed: resp=%+v err=%v", resp, err)
			}
			if resp.Parsed == nil || resp.Parsed.Usage == nil || *resp.Parsed.Usage.OutputTokens != 4 {
				t.Fatal("incomplete output lost accounting")
			}
		})
	}
}
