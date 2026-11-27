package bedrockresponses

import (
	"testing"

	"github.com/inferplane/inferplane/providers"
)

func TestMantleResponsesUpstreamDecidesContext(t *testing.T) {
	if !providers.EnforcesContextWindow(&provider{}, "openai.gpt-6.2-astra") {
		t.Fatal("Mantle enforces the window and reports context_length_exceeded itself")
	}
}
