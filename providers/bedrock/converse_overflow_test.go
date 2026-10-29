package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/inferplane/inferplane/providers"
)

// Captured from Bedrock Converse for openai.gpt-oss-120b-1:0 on 2026-10-27
// (mayu server log): the non-streaming and ConverseStream rejections of a
// 484 KB request whose real size was 135,043 tokens.
const (
	converseOverflow       = `The model returned the following errors: ErrorEvent { error: APIError { type: "BadRequestError", code: Some(400), message: "Input length (135043) exceeds model's maximum context length (131072).", param: None } }`
	converseStreamOverflow = `The model returned the following errors: Mantle streaming error for requestId 05a72250-6b0b-4e21-9207-b29de3146875: ErrorEvent { error: APIError { type: "BadRequestError", code: Some(400), message: "Input length (140026) exceeds model's maximum context length (131072).", param: None } }`
	// Observed for gpt-6 family models: the relayed OpenAI error envelope.
	converseMinTokens = `The model returned the following errors: {"error":{"code":"integer_below_min_value","message":"Invalid 'max_output_tokens': integer below minimum value. Expected a value >= 16, but got 1 instead.","param":"max_output_tokens","type":"invalid_request_error"}}`
)

func overflowMessage(t *testing.T, err error) (status int, code, message string) {
	t.Helper()
	var upstream *providers.UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("not an upstream error: %v", err)
	}
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if json.Unmarshal(upstream.Body, &body) != nil {
		t.Fatalf("unreadable body: %s", upstream.Body)
	}
	return upstream.StatusCode, body.Error.Code, body.Error.Message
}

func TestConverseOverflowBecomesRecoverablePromptTooLong(t *testing.T) {
	for _, tc := range []struct{ name, upstream, want string }{
		{"converse", converseOverflow, "prompt is too long: 135043 tokens > 131072 maximum"},
		{"converse stream", converseStreamOverflow, "prompt is too long: 140026 tokens > 131072 maximum"},
		{"relayed openai code", `The model returned the following errors: {"error":{"code":"context_length_exceeded","message":"private prompt text","param":"input","type":"invalid_request_error"}}`, "prompt is too long"},
		{"claude on converse", "prompt is too long: 250001 tokens > 200000 maximum", "prompt is too long: 250001 tokens > 200000 maximum"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				sdkErr := fmt.Errorf("bedrock: converse %q: %w", "m", &brtypes.ValidationException{Message: aws.String(tc.upstream)})
				p := &provider{conv: &fakeConverser{err: sdkErr}, modelAPI: map[string]string{"m": "converse"}}
				req := &providers.ProxyRequest{Model: "m", Upstream: "m", RawBody: []byte(`{"messages":[{"role":"user","content":"hi"}]}`), Stream: stream}
				var err error
				if stream {
					_, err = p.Stream(context.Background(), req)
				} else {
					_, err = p.Complete(context.Background(), req)
				}
				status, code, message := overflowMessage(t, err)
				if status != 400 || code != "context_length_exceeded" || message != tc.want {
					t.Fatalf("got %d %q %q, want 400 context_length_exceeded %q", status, code, message, tc.want)
				}
			})
		}
	}
}

func TestConverseNonOverflowValidationStaysScrubbed(t *testing.T) {
	for _, upstream := range []string{
		converseMinTokens,
		strings.Replace(converseOverflow, "(131072).", "(131072). arn:aws:iam::123456789012:role/private", 1),
		strings.Replace(converseOverflow, "Input length", "Output length", 1),
		strings.Replace(converseOverflow, "BadRequestError", "InternalError", 1),
		"The model returned the following errors: " + strings.Repeat("x", 70<<10),
	} {
		got := upstreamError(&brtypes.ValidationException{Message: aws.String(upstream)})
		if providers.ResponseErrorCode(got.Body) != "" || strings.Contains(string(got.Body), "arn:aws") ||
			strings.Contains(string(got.Body), "Invalid 'max_output_tokens'") || got.StatusCode != 400 {
			t.Fatalf("unrecognized validation was classified or leaked: %s", got.Body)
		}
	}
}

func TestNativeConverseOverflowBecomesRecoverablePromptTooLong(t *testing.T) {
	for _, tc := range []struct {
		name, header, body string
		recognized         bool
	}{
		{"typed header", "ValidationException:http://internal.amazon.com/coral/com.amazon.bedrock/", mustJSON(t, map[string]string{"message": converseOverflow}), true},
		{"typed body", "", mustJSON(t, map[string]string{"__type": "com.amazon.bedrock#ValidationException", "message": converseStreamOverflow}), true},
		{"untyped", "", mustJSON(t, map[string]string{"message": converseOverflow}), false},
		{"other validation", "ValidationException", mustJSON(t, map[string]string{"message": converseMinTokens}), false},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if tc.header != "" {
						w.Header().Set("X-Amzn-ErrorType", tc.header)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(tc.body))
				})
				req := &providers.ProxyRequest{Upstream: "m", RawBody: []byte(`{"messages":[]}`)}
				var err error
				if stream {
					_, err = n.Stream(context.Background(), req)
				} else {
					_, err = n.Complete(context.Background(), req)
				}
				status, code, message := overflowMessage(t, err)
				if !tc.recognized {
					if code != "" || message != "bedrock Converse upstream rejected the request" || status != 400 {
						t.Fatalf("unrecognized native error classified or leaked: %d %q %q", status, code, message)
					}
					return
				}
				if status != 400 || code != "context_length_exceeded" || !strings.HasPrefix(message, "prompt is too long: 1") {
					t.Fatalf("native overflow not recoverable: %d %q %q", status, code, message)
				}
			})
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
