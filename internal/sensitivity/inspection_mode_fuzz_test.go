package sensitivity

import "testing"

func FuzzInspectionModesPreserveStructure(f *testing.F) {
	for _, seed := range []struct{ protocol, raw string }{
		{"anthropic", `{"messages":[{"role":"user","content":"person@example.test"}],"max_tokens":32}`},
		{"openai", `{"messages":[{"role":"user","content":"{\"nested\":4111111111111111}"}]}`},
		{"responses", `{"input":[{"type":"item_reference","id":"synthetic"}]}`},
		{"bedrock", `{"messages":[{"role":"user","content":"fixture"}],"max_tokens":32}`},
		{"bedrock-converse", `{"messages":[{"role":"user","content":[{"text":"fixture"}]}],"inferenceConfig":{"maxTokens":32}}`},
		{"openai", `{"duplicate":1,"duplicate":2}`},
		{"unknown", `null`},
	} {
		f.Add(seed.protocol, []byte(seed.raw))
	}
	f.Fuzz(func(t *testing.T, protocol string, raw []byte) {
		if len(raw) > 64<<10 || len(protocol) > 64 {
			t.Skip()
		}
		compareInspectionModes(t, protocol, raw)
	})
}
