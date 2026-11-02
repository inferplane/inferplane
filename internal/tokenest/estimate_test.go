package tokenest

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/inferplane/inferplane/internal/tokenest/tokenesttest"
)

// realTokens are o200k_base counts (tiktoken 0.14.0) of tokenesttest's text,
// recorded when the estimator was calibrated. gpt-oss on Bedrock uses this
// tokenizer; its Converse rejection reported the measured dense case exactly
// ("Input length (135043)"). The generators are deterministic, so a changed
// count means a changed generator: re-measure instead of editing the number.
var realTokens = map[string]int64{
	"prose": 30155, "code": 47210, "read": 55373, "digits": 60176, "hex": 116292, "hangul": 137787, "korean": 46139,
}

func ratio(got, want int64) float64 { return float64(got) / float64(want) }

func TestEstimateTracksTokenizerPerClass(t *testing.T) {
	for _, c := range tokenesttest.Classes {
		t.Run(c.Name, func(t *testing.T) {
			got := Estimate("anthropic", tokenesttest.AnthropicBody("m", c.Text(1, 200_000)))
			low, high := 0.97, 1.03
			switch c.Name {
			case "hex":
				low, high = 0.94, 1.06
			case "korean":
				// Natural Korean is ≈0.77 tokens per syllable on o200k; the
				// 0.8 rate leaves room for less Korean-efficient tokenizers.
				low, high = 1.00, 1.20
			case "hangul":
				// Known limit: uniformly random syllables are mostly rare and
				// byte-encoded (≈2 tokens each), so they are under-estimated;
				// an upstream overflow is still recoverable (errors.go).
				low, high = 0.30, 0.42
			}
			if r := ratio(got, realTokens[c.Name]); r < low || r > high {
				t.Fatalf("estimate %d vs tokenizer %d: ratio %.3f outside [%.2f, %.2f]", got, realTokens[c.Name], r, low, high)
			}
		})
	}
}

// The 2026-10-27 path check measured these whole requests through the
// gateway. bytes/4 rejected the prose case at 76% of the real window and let
// the dense case through to a Bedrock rejection.
func TestEstimateReproducesMeasuredCases(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     []byte
		measured int64
	}{
		{"prose 624 KB", tokenesttest.AnthropicBody("m", tokenesttest.ClassNamed("prose").Text(2, 624_000)), 97_000},
		{"dense 484 KB", tokenesttest.AnthropicBody("m", tokenesttest.Dense(3, 484_000)), 135_043},
		{"dense 398 KB", tokenesttest.AnthropicBody("m", tokenesttest.Dense(3, 398_000)), 111_165},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Estimate("anthropic", tc.body)
			if r := ratio(got, tc.measured); r < 0.88 || r > 1.12 {
				t.Fatalf("estimate %d vs measured %d: ratio %.3f outside ±12%%", got, tc.measured, r)
			}
			if old := int64(len(tc.body) / 4); tc.name == "prose 624 KB" && ratio(old, tc.measured) < 1.5 {
				t.Fatalf("fixture no longer reproduces the bytes/4 over-estimate: %d", old)
			}
		})
	}
}

func TestEstimateTextRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want int64 // tokens of the text alone
	}{
		{"word absorbs one space", "hello world", 2},
		{"camelCase splits", "requestHandler", 2},
		{"long word splits every eight letters", "internationalization", 3},
		{"digits group by three", "1234567", 3},
		{"space before digits is its own token", "id 123", 3},
		{"single punctuation joins a word", ".Context", 1},
		{"punctuation run", "\");", 2},
		{"newlines", "a\n\n\nb", 3},
		{"escaped newline and tab decode", `a\n\tb`, 3},
		{"hangul syllables", "한국어", 3},
		{"cjk", "日本語", 3},
		{"accented letters stay in the word", "Größe", 1},
		{"symbol", "→", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (decodedCost([]byte(tc.text)) + 99) / 100; got != tc.want {
				t.Fatalf("decodedCost(%q) = %d tokens, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// bs is a backslash built at run time so the escapes below stay escapes in
// this source file.
var bs = string(rune(92))

func TestEstimateDecodesJSONEscapes(t *testing.T) {
	for _, tc := range []struct{ escaped, literal string }{
		{bs + "u00e9t" + bs + "u00e9", "été"},
		{bs + "ud83d" + bs + "ude42", "🙂"},
		{bs + "uD83D" + bs + "uDE42", "🙂"},
		{bs + "ud55c" + bs + "uad6d" + bs + "uc5b4", "한국어"},
	} {
		if !strings.Contains(tc.escaped, bs+"u") {
			t.Fatal("escape lost from test source")
		}
		if got, want := decodedCost([]byte(tc.escaped)), decodedCost([]byte(tc.literal)); got != want {
			t.Fatalf("decodedCost(%q) = %d, want %d (the literal %q)", tc.escaped, got, want, tc.literal)
		}
	}
	for _, lone := range []string{bs + "ud83d", bs + "ude42", bs + "ud83d" + bs + "u0041", bs + "u12", bs + "uzzzz"} {
		if got := (decodedCost([]byte(lone)) + 99) / 100; got > 3 {
			t.Fatalf("decodedCost(%q) = %d tokens", lone, got)
		}
	}
	for _, lone := range []string{bs + "ud83d", bs + "ude42", bs + "ud83d" + bs + "u0041"} {
		if got := (decodedCost([]byte(lone)) + 99) / 100; got > 1 {
			t.Fatalf("lone surrogate %q = %d tokens, want at most 1", lone, got)
		}
	}
}

// asciiEscape writes every non-ASCII rune as JSON \uXXXX escapes, the way an
// ASCII-only encoder (Python json.dumps default) would.
func asciiEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r > 0xFFFF:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&b, "%su%04x%su%04x", bs, hi, bs, lo)
		default:
			fmt.Fprintf(&b, "%su%04x", bs, r)
		}
	}
	return b.String()
}

func TestEstimateEscapedBodyMatchesLiteralBody(t *testing.T) {
	text := tokenesttest.ClassNamed("korean").Text(1, 50_000) + " 🙂 été"
	lit := tokenesttest.AnthropicBody("m", text)
	esc := []byte(asciiEscape(string(lit)))
	if !strings.Contains(string(esc), bs+"u") || len(esc) <= len(lit) {
		t.Fatal("escape lost from test source")
	}
	if a, b := Estimate("anthropic", lit), Estimate("anthropic", esc); a != b {
		t.Fatalf("escaped body = %d, literal body = %d", b, a)
	}
}

// Signatures, encrypted_content and documents are long base64-alphabet
// strings but never inline images, so they must not start the structural
// image pass (a full encoding/json decode of the body).
func TestEstimateSkipsImagePassWithoutImageKeys(t *testing.T) {
	sig := strings.Repeat("aGVsbG8gd29ybGQh", 40) // 640 base64-alphabet bytes
	var b strings.Builder
	b.WriteString(`{"model":"m","messages":[`)
	for i := 0; i < 100; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"%s"},{"type":"reasoning","encrypted_content":"%s"},{"type":"document","source":{"type":"text","media_type":"text/plain","content":"%s"}}]}`, sig, sig, sig)
	}
	b.WriteString(`]}`)
	body := []byte(b.String())
	if allocs := testing.AllocsPerRun(5, func() { Estimate("anthropic", body) }); allocs > 20 {
		t.Fatalf("%v allocations: the image pass ran without an image key", allocs)
	}
}

func TestEstimateStructure(t *testing.T) {
	if got := Estimate("anthropic", nil); got != 1 {
		t.Fatalf("empty body = %d, want 1", got)
	}
	if got := Estimate("anthropic", []byte(`{"text":`)); got != (textCost([]byte(`{"text":`))+99)/100 {
		t.Fatalf("invalid JSON must be costed as plain text: %d", got)
	}
	a := Estimate("anthropic", []byte(`{"model":"m","messages":[{"role":"user","content":"hi there"}]}`))
	b := Estimate("anthropic", []byte(`{"messages":[{"content":"hi there","role":"user"}],"model":"m"}`))
	if a != b {
		t.Fatalf("member order changed the estimate: %d vs %d", a, b)
	}
	// keys: model, messages, role, content (2 each) + values m, user, "hi there" (1+1+2)
	if a != 12 {
		t.Fatalf("structure estimate = %d, want 12", a)
	}
}

func image(data string) string {
	return `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}`
}

func TestEstimateChargesInlineImagesAtTheirNominalCost(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("image-payload", 4000)))
	text := `{"type":"text","text":"describe it"}`
	for _, tc := range []struct{ protocol, body string }{
		{"anthropic", `{"messages":[{"role":"user","content":[` + image(data) + `,` + text + `]}]}`},
		// Claude Code's Read tool returns a screenshot inside a tool result,
		// often with a cache breakpoint on the block.
		{"anthropic", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[` +
			strings.Replace(image(data), `{"type":"image",`, `{"type":"image","cache_control":{"type":"ephemeral"},`, 1) + `]},` + text + `]}]}`},
		{"bedrock", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":[` + image(data) + `,` + text + `]}]}]}`},
		{"bedrock-converse", `{"messages":[{"role":"user","content":[{"image":{"format":"png","source":{"bytes":"` + data + `"}}},{"text":"describe it"}]}]}`},
		{"bedrock-converse", `{"messages":[{"role":"user","content":[{"toolResult":{"toolUseId":"call","content":[{"image":{"format":"jpeg","source":{"bytes":"` + data + `"}}}]}},{"text":"describe it"}]}]}`},
		{"openai", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + data + `","detail":"high"}},` + text + `]}]}`},
		{"responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + data + `"},{"type":"input_text","text":"describe it"}]}]}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			got := Estimate(tc.protocol, []byte(tc.body))
			if got < imageTokens || got > imageTokens+60 {
				t.Fatalf("image request = %d tokens, want the %d-token image ceiling plus small text", got, imageTokens)
			}
			if old := int64(len(tc.body) / 4); old < 3*imageTokens {
				t.Fatalf("fixture too small to show the base64 discount: bytes/4 = %d", old)
			}
		})
	}
}

func TestEstimateKeepsOpaquePayloadRate(t *testing.T) {
	data := strings.Repeat("aGVsbG8gd29ybGQh", 1024) // valid base64, 16 KiB
	opaque := int64(len(data) / 4)
	for _, body := range []string{
		`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + data + `"}}]}]}`,
		`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"n","input":` + image(data) + `}]}]}`,
		`{"tools":[{"name":"n","input_schema":` + image(data) + `}]}`,
		`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"` + data + `"}]}]}`,
		`{"messages":[{"role":"system","content":[` + image(data) + `]}]}`,
		`{"messages":[{"role":"user","content":[` + strings.Replace(image(data), `"image/png"`, `"application/pdf"`, 1) + `]}]}`,
	} {
		got := Estimate("anthropic", []byte(body))
		if got < opaque || got > opaque+60 {
			t.Fatalf("opaque payload = %d tokens, want about %d (four bytes per token): %.80s", got, opaque, body)
		}
	}
	invalid := strings.Repeat("aGVsbG8=", 2048) // '=' inside: invalid base64, still the base64 alphabet
	if got := Estimate("anthropic", []byte(`{"messages":[{"role":"user","content":[`+image(invalid)+`]}]}`)); got < int64(len(invalid)/4) {
		t.Fatalf("invalid image payload was discounted: %d", got)
	}
}

func BenchmarkEstimateDense16MiB(b *testing.B) {
	body := tokenesttest.AnthropicBody("m", tokenesttest.Dense(9, 16<<20))
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		Estimate("anthropic", body)
	}
}
