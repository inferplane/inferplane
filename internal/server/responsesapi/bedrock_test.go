package responsesapi

import (
	"testing"

	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

type bedrockBridgeProbe struct {
	providers.Provider
	supported bool
}

func (*bedrockBridgeProbe) Name() string { return "bedrock" }
func (p *bedrockBridgeProbe) SupportsIngress(protocol string) bool {
	return p.supported && protocol == "responses"
}

func TestBedrockBridgeAdmissionRequiresPortableRequest(t *testing.T) {
	p := &bedrockBridgeProbe{Provider: mockprovider.New("unused"), supported: true}
	target := router.ChainTarget{Provider: p, Model: "grok"}
	for _, tt := range []struct {
		name, body string
		want       bool
	}{
		{"text", `{"model":"grok","input":"hello"}`, true},
		{"tools", `{"model":"grok","input":"hello","tools":[{"type":"function","name":"read","strict":false,"parameters":{"type":"object"}}]}`, true},
		{"strict", `{"model":"grok","input":"hello","tools":[{"type":"function","name":"read","strict":true,"parameters":{"type":"object"}}]}`, false},
		{"implicit strict", `{"model":"grok","input":"hello","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`, false},
		{"remote state", `{"model":"grok","input":"hello","previous_response_id":"resp_owned"}`, false},
		{"opaque reasoning", `{"model":"grok","input":[{"type":"reasoning","encrypted_content":"opaque","summary":[]}]}`, false},
		{"hosted tool", `{"model":"grok","input":"hello","tools":[{"type":"web_search"}]}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := compatibleTarget([]byte(tt.body), target, nil); got != tt.want {
				t.Fatalf("compatible = %t, want %t", got, tt.want)
			}
		})
	}
	p.supported = false
	if compatibleTarget([]byte(`{"model":"grok","input":"hello"}`), target, nil) {
		t.Fatal("provider opted out but was admitted")
	}
}
