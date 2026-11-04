package bedrock

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/inferplane/inferplane/providers"
)

func TestTypedPromptOverflowRetainsOnlySafeRecoveryEvidence(t *testing.T) {
	err := &brtypes.ValidationException{Message: aws.String("prompt is too long: 250001 tokens > 200000 maximum")}
	got := upstreamError(err)
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(got.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if got.StatusCode != 400 || providers.ResponseErrorCode(got.Body) != "context_length_exceeded" ||
		payload.Error.Message != "prompt is too long: 250001 tokens > 200000 maximum" {
		t.Fatalf("typed context refusal lost: %s", got.Body)
	}
}

func TestPromptOverflowClassifierRejectsUntrustedOrMalformedDetails(t *testing.T) {
	for _, err := range []error{
		errors.New("prompt is too long: 250001 tokens > 200000 maximum"),
		&brtypes.ValidationException{Message: aws.String("prompt is too long: 250001 tokens > 200000 maximum arn:aws:private")},
		&brtypes.ValidationException{Message: aws.String("prompt is too long: 1 tokens > 200000 maximum")},
		&brtypes.ValidationException{Message: aws.String("prompt is too long: 99999999999999999999999999 tokens > 1 maximum")},
	} {
		got := upstreamError(err)
		if providers.ResponseErrorCode(got.Body) != "" || strings.Contains(string(got.Body), "arn:aws") {
			t.Fatalf("untrusted details classified or leaked: %s", got.Body)
		}
	}
}

// A recognized overflow leaves one operator log line with the status and the
// validated counts only: no provider text reaches the log either.
func TestPromptOverflowLogsOnlyValidatedCounts(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	upstreamError(&brtypes.ValidationException{Message: aws.String("prompt is too long: 250001 tokens > 200000 maximum")})
	upstreamError(&brtypes.ValidationException{Message: aws.String(relayedErrorPrefix +
		`{"error":{"code":"context_length_exceeded","message":"private prompt text","param":"input","type":"invalid_request_error"}}`)})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 ||
		!strings.Contains(lines[0], "bedrock: upstream error status=400 code=ValidationException: prompt overflow tokens=250001 maximum=200000") ||
		!strings.Contains(lines[1], "bedrock: upstream error status=400 code=ValidationException: prompt overflow tokens=0 maximum=0") ||
		strings.Contains(buf.String(), "prompt is too long") || strings.Contains(buf.String(), "private prompt text") {
		t.Fatalf("overflow log = %q, want one line per overflow with validated counts only", buf.String())
	}
}
