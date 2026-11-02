// Package tokenesttest generates the deterministic synthetic request bodies
// that calibrate internal/tokenest. Each class mirrors one content class of
// the 2026-10-27 gateway path measurement (bytes per token there: prose 6.3,
// code-shaped lines 4.8, Claude Code Read output ≈4.6, digit-heavy 2.9–3.5,
// hex 1.7, Hangul 1.3), plus natural Korean prose. Nothing here is compiled
// into a binary.
package tokenesttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"unicode/utf8"
)

// Class is one content class.
type Class struct {
	Name string
	gen  func(r *rand.Rand, b *strings.Builder)
}

// Classes lists the content classes; "hangul" is uniformly random syllables
// (the measured dense case), "korean" is natural Korean prose.
var Classes = []Class{
	{"prose", prose},
	{"code", code},
	{"read", read},
	{"digits", digits},
	{"hex", hexLines},
	{"hangul", hangul},
	{"korean", korean},
}

// ClassNamed returns the class with name; it panics on an unknown name.
func ClassNamed(name string) Class {
	for _, c := range Classes {
		if c.Name == name {
			return c
		}
	}
	panic("tokenesttest: unknown class " + name)
}

// Text returns exactly n bytes (at a rune boundary, so possibly a few fewer)
// of class text generated from seed.
func (c Class) Text(seed uint64, n int) string {
	r := rand.New(rand.NewPCG(seed, 0x746f6b656e657374))
	var b strings.Builder
	for b.Len() < n {
		c.gen(r, &b)
	}
	return truncate(b.String(), n)
}

// denseShares are byte shares chosen so the measured class rates combine to
// the measured 3.58 bytes per token of the dense requests (484 KB → 135,043
// and 398 KB → 111,165 real tokens); on o200k_base this mixture is within 1%
// of both.
var denseShares = []struct {
	class string
	share int // percent of bytes
}{{"prose", 25}, {"code", 30}, {"digits", 35}, {"hex", 5}, {"hangul", 5}}

// Dense interleaves 4 KiB chunks of the dense mixture, n bytes in total.
func Dense(seed uint64, n int) string {
	var b strings.Builder
	for chunk := uint64(0); b.Len() < n; chunk++ {
		for _, s := range denseShares {
			b.WriteString(ClassNamed(s.class).Text(seed+chunk*7919+uint64(s.share), 4096*s.share/100))
		}
	}
	return truncate(b.String(), n)
}

// AnthropicBody wraps text as one user text block of a Messages request,
// encoded like a JavaScript client (no HTML escaping).
func AnthropicBody(model, text string) []byte {
	return encode(map[string]any{
		"model": model, "max_tokens": 1024,
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": text},
		}}},
	})
}

func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

var words = strings.Fields(`the system request gateway model context window budget policy
provider routing response stream token client server session history message
content record account network service traffic control plane measure limit
between before through during against within without under around across
people report answer change number second little public country general
however always several already another because example certain question
process develop support provide include continue consider follow present`)

func prose(r *rand.Rand, b *strings.Builder) {
	for s := 0; s < 6; s++ {
		n := 10 + r.IntN(9)
		for i := 0; i < n; i++ {
			w := words[r.IntN(len(words))]
			if i == 0 {
				w = strings.ToUpper(w[:1]) + w[1:]
			} else {
				b.WriteByte(' ')
			}
			b.WriteString(w)
			if i > 1 && i < n-2 && r.IntN(9) == 0 {
				b.WriteByte(',')
			}
		}
		b.WriteString(". ")
	}
	b.WriteString("\n\n")
}

var idents = strings.Fields(`request response handler config router target model
provider budget window stream reader writer buffer record cache token limit
status result value index count state`)

func ident(r *rand.Rand) string {
	a, c := idents[r.IntN(len(idents))], idents[r.IntN(len(idents))]
	return a + strings.ToUpper(c[:1]) + c[1:]
}

func codeLine(r *rand.Rand) string {
	switch r.IntN(8) {
	case 0:
		return fmt.Sprintf("func (h *%s) %s(ctx context.Context, %s []byte) (int64, error) {", ident(r), ident(r), ident(r))
	case 1:
		return fmt.Sprintf("\tif err := %s.%s(%s); err != nil {", ident(r), ident(r), ident(r))
	case 2:
		return fmt.Sprintf("\t\treturn 0, fmt.Errorf(\"%s: %%w\", err)", ident(r))
	case 3:
		return "\t}"
	case 4:
		return fmt.Sprintf("\t%s := %s[%d]", ident(r), ident(r), r.IntN(64))
	case 5:
		return fmt.Sprintf("\t// %s keeps the %s %s for the %s.", ident(r), words[r.IntN(len(words))], words[r.IntN(len(words))], words[r.IntN(len(words))])
	case 6:
		return fmt.Sprintf("\tfor _, %s := range %s.%s {", ident(r), ident(r), ident(r))
	default:
		return fmt.Sprintf("\t%s.%s = append(%s.%s, %s)", ident(r), ident(r), ident(r), ident(r), ident(r))
	}
}

func code(r *rand.Rand, b *strings.Builder) {
	b.WriteString(codeLine(r))
	b.WriteByte('\n')
}

// read mirrors Claude Code's Read tool output: a right-aligned line number,
// an arrow and the file line.
func read(r *rand.Rand, b *strings.Builder) {
	fmt.Fprintf(b, "%6d→%s\n", 1+r.IntN(5000), codeLine(r))
}

func digits(r *rand.Rand, b *strings.Builder) {
	fmt.Fprintf(b, "id %012d total %09d status %s\n", r.Int64N(1e12), r.Int64N(1e9), words[r.IntN(len(words))])
}

func hexLines(r *rand.Rand, b *strings.Builder) {
	const digits = "0123456789abcdef"
	for i := 0; i < 64; i++ {
		b.WriteByte(digits[r.IntN(16)])
	}
	b.WriteByte('\n')
}

func hangul(r *rand.Rand, b *strings.Builder) {
	for w := 0; w < 16; w++ {
		if w > 0 {
			b.WriteByte(' ')
		}
		for s := 0; s < 2+r.IntN(4); s++ {
			b.WriteRune(rune(0xAC00 + r.IntN(11172)))
		}
	}
	b.WriteByte('\n')
}

const koreanParagraph = "게이트웨이는 요청 본문을 읽은 뒤 모델의 컨텍스트 창과 비교합니다. 추정치가 창보다 크면 업스트림을 부르지 않고 바로 거절하므로 비용이 들지 않습니다. 클라이언트는 이 오류를 보고 대화를 압축한 다음 다시 보냅니다. 압축은 손실이 있으므로 너무 일찍 거절하면 답변의 정확도가 떨어집니다. 반대로 너무 늦게 거절하면 업스트림이 거절하고, 그 오류 문구를 클라이언트가 이해하지 못하면 세션이 끝납니다. 그래서 추정은 실제 토큰 수에 가까워야 하고, 업스트림의 거절도 같은 형식으로 바꿔 전달해야 합니다. 운영자는 메트릭과 감사 기록으로 거절 이유를 확인할 수 있습니다."

// korean numbers repetitions of one natural paragraph; repetition does not
// change how a tokenizer splits it.
func korean(r *rand.Rand, b *strings.Builder) {
	fmt.Fprintf(b, "%d. %s\n\n", 1+r.IntN(1000), koreanParagraph)
}
