package responses

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/inferplane/inferplane/internal/openai"
)

func TestNoToolCompactionAllowsNonParallelPreference(t *testing.T) {
	for _, tools := range []string{"", `,"tools":[]`, `,"tools":[ ]`, `,"tools":null`} {
		raw := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"summarize this synthetic history"}]}],"stream":true,"parallel_tool_calls":false,"tool_choice":"auto","reasoning":{"effort":"none"},"store":false,"include":["reasoning.encrypted_content"]` + tools + `}`)
		if err := ValidateConversion(raw); err != nil {
			t.Fatalf("no-tool compaction rejected (%s): %v", tools, err)
		}
		req, err := RequestToCanonical(raw)
		if err != nil {
			t.Fatal(err)
		}
		wire := openai.CanonicalToRequest(req)
		if !bytes.Contains(wire, []byte("summarize this synthetic history")) {
			t.Fatal("compaction history was lost")
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(wire, &body); err != nil {
			t.Fatal(err)
		}
		if len(body["tools"]) > 0 && string(body["tools"]) != "[]" {
			t.Fatalf("compaction invented tools: %s", body["tools"])
		}
	}
}

func TestNoToolParallelPreferenceDoesNotRelaxUnsupportedFeatures(t *testing.T) {
	for _, extra := range []string{
		`"tools":[{"type":"function","name":"read","strict":false,"parameters":{"type":"object"}}]`,
		`"tools":[{"type":"namespace","name":"local","tools":[]}]`,
		`"tools":[{"type":"web_search"}]`,
		`"previous_response_id":"resp_remote"`,
		`"reasoning":{"effort":"high"}`,
		`"input":[{"type":"reasoning","encrypted_content":"opaque"}]`,
		`"tools":{}`,
	} {
		raw := []byte(`{"model":"m","parallel_tool_calls":false,` + extra + `}`)
		if err := ValidateConversion(raw); err == nil {
			t.Fatalf("unsupported requirement accepted: %s", raw)
		}
	}
	for _, parallel := range []string{`"false"`, `0`, `{}`} {
		raw := []byte(`{"model":"m","input":"hello","tools":[],"parallel_tool_calls":` + parallel + `}`)
		if err := ValidateConversion(raw); err == nil {
			t.Fatalf("malformed parallel preference accepted: %s", raw)
		}
	}
}
