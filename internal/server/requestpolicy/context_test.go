package requestpolicy

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestContextTextEstimateSeparatesInlineImageTransport(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("image-payload", 1000)))
	image := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}`
	for _, tc := range []struct{ protocol, body string }{
		{"anthropic", `{"messages":[{"role":"user","content":[` + image + `]}]}`},
		{"bedrock", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":[` + image + `,{"type":"text","text":"describe it"}]}]}]}`},
		{"bedrock", `{"messages":[{"role":"user","content":[{"image":{"format":"png","source":{"bytes":"` + data + `"}}},{"text":"describe"}]}]}`},
		{"bedrock", `{"messages":[{"role":"user","content":[{"toolResult":{"toolUseId":"call","content":[{"image":{"format":"jpeg","source":{"bytes":"` + data + `"}}}]}}]}]}`},
		{"openai", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + data + `","detail":"high"}}]}]}`},
		{"responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + data + `"}]}]}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			raw := []byte(tc.body)
			want := max(int64(1), int64(len(raw)-len(data))/4)
			if got := ContextTextEstimate(tc.protocol, raw); got != want {
				t.Fatalf("text estimate = %d, want %d (base64 is transport, not text tokens)", got, want)
			}
			if string(raw) != tc.body {
				t.Fatal("estimation changed forwarded image bytes")
			}
		})
	}
}

func TestContextNativeImageDoesNotDiscountOpaqueFields(t *testing.T) {
	data := strings.Repeat("AAAA", 4096)
	for _, body := range []string{
		`{"messages":[{"role":"user","content":[{"image":{"format":"pdf","source":{"bytes":"` + data + `"}}}]}]}`,
		`{"messages":[{"role":"assistant","content":[{"toolUse":{"input":{"image":{"format":"png","source":{"bytes":"` + data + `"}}}}}]}]}`,
		`{"messages":[{"role":"user","content":[{"toolResult":{"content":[{"json":{"image":{"format":"png","source":{"bytes":"` + data + `"}}}}]}}]}]}`,
	} {
		if got := ContextTextEstimate("bedrock", []byte(body)); got != int64(len(body)/4) {
			t.Fatalf("opaque native content discounted: %d", got)
		}
	}
}

func TestContextTextEstimateDoesNotHideTextOrOpaqueData(t *testing.T) {
	data := strings.Repeat("aGVsbG8=", 1000)
	image := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}`
	quoted, _ := json.Marshal(image)
	for _, body := range []string{
		`{"messages":[{"role":"user","content":` + string(quoted) + `}]}`,
		`{"messages":[{"role":"assistant","content":[{"type":"tool_use","input":` + image + `}]}]}`,
		`{"tools":[{"input_schema":` + image + `}]}`,
		`{"messages":[{"role":"user","content":[` + strings.Replace(image, `"type":"image"`, `"type":"document"`, 1) + `]}]}`,
		`{"messages":[{"role":"user","content":[` + strings.Replace(image, `"type":"base64"`, `"type":"unknown"`, 1) + `]}]}`,
		`{"messages":[{"role":"user","content":[` + image + `]}]}`, // invalid interior base64 padding
		`{"messages":[{"role":"user","content":[` + strings.Replace(image, `"image/png"`, `"application/pdf"`, 1) + `]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"thinking","signature":"` + data + `"}]}]}`,
		`{"messages":[{"role":"user","content":[` + image + `]}]`, // invalid JSON
	} {
		raw := []byte(body)
		if got, want := ContextTextEstimate("anthropic", raw), int64(len(raw)/4); got != want {
			t.Fatalf("text/unsupported data was discounted: got %d want %d", got, want)
		}
	}
}

func TestContextTextEstimateRetainsTextBesideImages(t *testing.T) {
	data := strings.Repeat("AAAA", 4096)
	raw := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}},{"type":"text","text":"` + strings.Repeat("text ", 1000) + `"}]}]}`)
	if got := ContextTextEstimate("anthropic", raw); got < 1250 {
		t.Fatalf("visible text was lost beside an image: %d", got)
	}
	if got := ContextTextEstimate("unknown", raw); got != int64(len(raw)/4) {
		t.Fatal("unknown protocol was treated as understood")
	}
}
