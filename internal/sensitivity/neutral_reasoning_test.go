package sensitivity

import (
	"context"
	"slices"
	"testing"
)

func TestResponsesNeutralEffortDoesNotRequireReasoningModel(t *testing.T) {
	got, err := NewInspector().Inspect(context.Background(), "responses",
		[]byte(`{"model":"grok","input":"Contact alice@example.test","reasoning":{"effort":"none","summary":"auto"}}`))
	if err != nil || !got.Complete || got.HasReasoning || !slices.Contains(got.Categories, "email") {
		t.Fatalf("neutral request inspection = %+v, %v", got, err)
	}
	for _, raw := range []string{
		`{"model":"grok","input":"hello","reasoning":{"effort":"none","encrypted_content":"opaque"}}`,
		`{"model":"grok","input":[{"type":"reasoning","encrypted_content":"opaque"}],"reasoning":{"effort":"none"}}`,
	} {
		got, err := NewInspector().Inspect(context.Background(), "responses", []byte(raw))
		if err != nil || got.Complete {
			t.Fatalf("neutral preference hid opaque data: %+v, %v", got, err)
		}
	}
}
