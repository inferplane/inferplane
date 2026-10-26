package openai

import (
	"encoding/json"
	"testing"
)

func TestUsageInjectionAcceptsNullAndPreservesAdditionalOptions(t *testing.T) {
	for _, options := range []string{`null`, `{"include_usage":false,"include_obfuscation":false}`} {
		t.Run(options, func(t *testing.T) {
			top := map[string]json.RawMessage{"stream_options": []byte(options)}
			EnsureIncludeUsage(top)
			var so map[string]any
			if err := json.Unmarshal(top["stream_options"], &so); err != nil {
				t.Fatal(err)
			}
			if so["include_usage"] != true || (options != "null" && so["include_obfuscation"] != false) {
				t.Fatalf("invalid usage merge: %s", top["stream_options"])
			}
		})
	}
}
