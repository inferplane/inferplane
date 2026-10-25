package requestpolicy

import (
	"encoding/json"
	"testing"

	"github.com/inferplane/inferplane/pkg/schema"
)

func TestCacheQuantityDoesNotProveMonetaryRefund(t *testing.T) {
	for _, raw := range []string{
		`{"input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":40}}`,
		`{"input_tokens":2,"output_tokens":3,"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":40,"ephemeral_1h_input_tokens":60,"ephemeral_30m_input_tokens":1}}`,
	} {
		var u schema.Usage
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			t.Fatal(err)
		}
		if _, exact, _ := authorityUsage(&u, 1000); exact {
			t.Fatal("unknown TTL proved a monetary refund")
		}
		if n, exact, invalid := authorityTokens(&u, 1000); n != 105 || !exact || invalid {
			t.Fatalf("tokens=%d exact=%v invalid=%v", n, exact, invalid)
		}
	}
}

func TestConverseVendorFieldsCannotMultiplyGovernedGenerations(t *testing.T) {
	for _, raw := range []string{
		`{"messages":[],"additionalModelRequestFields":{"n":2}}`,
		`{"messages":[],"additionalModelRequestFields":{"num_generations":2}}`,
		`{"messages":[],"additionalModelRequestFields":{"n":null}}`,
	} {
		if singleGeneration([]byte(raw)) == nil {
			t.Fatalf("multiple or unknown generation count admitted: %s", raw)
		}
	}
	if err := singleGeneration([]byte(`{"messages":[{"content":[{"toolUse":{"input":{"n":20}}}]}],"additionalModelRequestFields":{"n":1}}`)); err != nil {
		t.Fatal("tool arguments incorrectly interpreted as generation controls")
	}
}
