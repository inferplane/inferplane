package bedrock

import (
	"encoding/json"
	"errors"
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
