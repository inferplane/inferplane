package bedrock

import (
	"testing"

	"github.com/inferplane/inferplane/providers"
)

// InvokeModel (Claude) maps the upstream's exact overflow verdict to the
// recoverable error, verified live. Converse families and the Mantle egress
// keep the gateway's own check until their overflow wording is verified.
func TestBedrockUpstreamDecidesContextForInvokeModelOnly(t *testing.T) {
	p := &provider{modelAPI: map[string]string{"x.mantle-model": "mantle"}}
	for upstream, want := range map[string]bool{
		"global.anthropic.claude-haiku-5-5": true,
		"global.openai.gpt-6.1-sol":         false,
		"x.mantle-model":                    false,
	} {
		if got := providers.EnforcesContextWindow(p, upstream); got != want {
			t.Errorf("%s: EnforcesContextWindow = %v, want %v", upstream, got, want)
		}
	}
}
