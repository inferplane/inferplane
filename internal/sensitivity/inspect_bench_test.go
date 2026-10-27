package sensitivity

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/inferplane/inferplane/internal/testutil/inspectionfixture"
)

func inspectionBenchmarkFixture(protocol string, bodyBytes int) []byte {
	return inspectionfixture.Request(protocol, bodyBytes)
}

func TestInspectionBenchmarkFixtures(t *testing.T) {
	for _, protocol := range []string{"anthropic", "openai", "responses", "bedrock", "bedrock-converse"} {
		for _, size := range []int{20 << 10, 200 << 10, 1 << 20, 8 << 20} {
			raw := inspectionBenchmarkFixture(protocol, size)
			if len(raw) != size || !json.Valid(raw) {
				t.Fatalf("%s/%d: fixture must be valid JSON of exactly the declared size", protocol, size)
			}
			view, err := NewInspector().Inspect(context.Background(), protocol, raw)
			if err != nil || !view.Complete || view.OutputTokens != 32 || len(view.Categories) != 0 {
				t.Fatalf("%s/%d: invalid clean synthetic fixture: %+v, %v", protocol, size, view, err)
			}
		}
	}
	for _, fixture := range []struct {
		name, protocol, raw string
		complete            bool
	}{
		{"nested-tool", "openai", `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"type":"function","function":{"name":"synthetic","arguments":"{\"nested\":{\"value\":\"fixture\"}}"}}]}]}`, true},
		{"opaque-response", "responses", `{"input":[{"type":"item_reference","id":"synthetic"}]}`, false},
		{"opaque-converse", "bedrock-converse", `{"messages":[{"role":"user","content":[{"image":{"format":"png","source":{"bytes":"YWJj"}}}]}]}`, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if !json.Valid([]byte(fixture.raw)) {
				t.Fatal("invalid fixture JSON")
			}
			view, err := NewInspector().Inspect(context.Background(), fixture.protocol, []byte(fixture.raw))
			if err != nil || view.Complete != fixture.complete || len(view.Categories) != 0 {
				t.Fatalf("fixture inspection: %+v, %v", view, err)
			}
		})
	}
}

func BenchmarkInspect(b *testing.B) {
	for _, mode := range []struct {
		name string
		mode InspectionMode
	}{{"full", DetectSensitiveData}, {"structure", StructureOnly}} {
		for _, protocol := range inspectionfixture.Protocols {
			for _, size := range inspectionfixture.Sizes {
				raw := inspectionBenchmarkFixture(protocol, size)
				for _, concurrency := range inspectionfixture.Concurrency {
					b.Run(fmt.Sprintf("%s/%s/bytes=%d/c=%d", mode.name, protocol, size, concurrency), func(b *testing.B) {
						inspector := NewInspector().(SelectiveInspector)
						inspectionfixture.Run(b, len(raw), concurrency, func() error {
							result, err := inspector.InspectWithMode(context.Background(), protocol, raw, mode.mode)
							if err == nil && (!result.Complete || result.OutputTokens != 32) {
								return fmt.Errorf("unexpected synthetic inspection outcome")
							}
							return err
						})
					})
				}
			}
		}
	}
}
