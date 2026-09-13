package responses

import "testing"

func TestNeutralReasoningPreferenceCanUsePortableBridge(t *testing.T) {
	for _, raw := range []string{
		`{"model":"grok","input":"hello","reasoning":{"effort":"none"}}`,
		`{"model":"grok","input":"hello","reasoning":{"effort":"none","summary":"auto"}}`,
	} {
		if err := ValidateConversion([]byte(raw)); err != nil {
			t.Fatalf("neutral preference refused: %v", err)
		}
		parsed, err := RequestToCanonical([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if len(parsed.Thinking) != 0 || parsed.Extra["reasoning"] != nil || parsed.Extra["reasoning_effort"] != nil {
			t.Fatal("neutral client preference became a backend reasoning requirement")
		}
	}
	for _, raw := range []string{
		`{"model":"grok","input":"hello","reasoning":{"effort":"high"}}`,
		`{"model":"grok","input":"hello","reasoning":{"effort":"none","encrypted_content":"opaque"}}`,
		`{"model":"grok","input":[{"type":"reasoning","encrypted_content":"opaque"}],"reasoning":{"effort":"none"}}`,
	} {
		if ValidateConversion([]byte(raw)) == nil {
			t.Fatal("neutral preference bypassed unsupported reasoning content")
		}
	}
}
