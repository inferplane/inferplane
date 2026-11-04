package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/providers"
)

// Bedrock Converse and native Converse now return providers.PromptTooLongBody
// for recognized overflow. Pin what each ingress's client receives: its own
// envelope, the recoverable code or wording, and no retry elsewhere.
func TestRecoverableOverflowOnEveryIngress(t *testing.T) {
	const want = "prompt is too long: 135043 tokens > 131072 maximum"
	ingresses := append(append([]policyIngress(nil), policyIngresses...),
		policyIngress{"responses", "/v1/responses", false}, policyIngress{"responses", "/v1/responses", true})
	for _, ing := range ingresses {
		t.Run(ing.name(), func(t *testing.T) {
			f := routingtest.New(t, nil)
			f.Public.Err = &providers.UpstreamError{StatusCode: 400, Body: providers.PromptTooLongBody(135043, 131072)}
			body := routingtest.Body(ing.protocol, "clean", ing.stream)
			if ing.protocol == "responses" {
				body = `{"model":"premium","stream":` + strconv.FormatBool(ing.stream) + `,"store":false,"max_output_tokens":16,"input":"hello"}`
			}
			rec := policyDo(policyMux(f, nil, nil, nil, nil), ing.path, body, true)
			var reply struct {
				Message string                               `json:"message"`
				Error   struct{ Type, Code, Message string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
				t.Fatalf("unreadable reply %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusBadRequest || len(f.Public.Calls) != 1 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, len(f.Public.Calls), rec.Body.String())
			}
			switch ing.protocol {
			case "bedrock":
				if rec.Header().Get("X-Amzn-ErrorType") != "ValidationException" || reply.Message != want {
					t.Fatalf("Bedrock client cannot recover: %s", rec.Body.String())
				}
			case "responses":
				if reply.Error.Code != "context_length_exceeded" || reply.Error.Type != "invalid_request_error" {
					t.Fatalf("Codex cannot compact: %s", rec.Body.String())
				}
			default: // Messages tees the Anthropic body; Chat clients read error.code
				if reply.Error.Message != want || reply.Error.Code != "context_length_exceeded" {
					t.Fatalf("client cannot recover: %s", rec.Body.String())
				}
			}
		})
	}
}
