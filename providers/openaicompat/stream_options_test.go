package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/inferplane/inferplane/providers"
)

func TestNativeUsageInjectionKeepsAdditionalStreamOptions(t *testing.T) {
	for _, options := range []string{`null`, `{"include_usage":false,"include_obfuscation":false,"vendor":{"format":"v1"}}`} {
		t.Run(options, func(t *testing.T) {
			raw := []byte(`{ "model":"public", "stream_options":` + options + `, "messages":[{"role":"user","content":"same cache prefix"}] }`)
			p := &provider{}
			got, err := p.buildBody(&providers.ProxyRequest{
				IngressProtocol: "openai", Upstream: "upstream", RawBody: raw,
			}, true)
			if err != nil {
				t.Fatal(err)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(got, &top); err != nil {
				t.Fatal(err)
			}
			var so map[string]json.RawMessage
			if err := json.Unmarshal(top["stream_options"], &so); err != nil {
				t.Fatal(err)
			}
			if string(so["include_usage"]) != "true" {
				t.Fatalf("usage request lost: %s", got)
			}
			if options != "null" && (string(so["include_obfuscation"]) != "false" || string(so["vendor"]) != `{"format":"v1"}`) {
				t.Fatalf("extra stream options lost: %s", got)
			}
			if string(top["messages"]) != `[{"role":"user","content":"same cache prefix"}]` {
				t.Fatalf("cache-bearing messages changed: %s", got)
			}
		})
	}
}
