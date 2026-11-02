// Package tokenest estimates how many input tokens a provider will count for a
// generation request body, offline and deterministically. It is not a
// tokenizer: it splits decoded JSON text into character-class runs the way BPE
// tokenizers pre-split text and charges each run a calibrated rate
// (estimate_test.go records the calibration against o200k_base and the
// 2026-10-27 gateway measurements). Inline images cost a fixed per-image
// ceiling; other base64 payloads keep the historical rate of four bytes per
// token. The package is a stdlib-only leaf.
package tokenest

import (
	"encoding/json"
	"unicode/utf8"
)

// imageTokens is the per-image ceiling: Claude resizes an image to at most
// about 1.15 megapixels (≈1,600 tokens) and OpenAI high detail stays below it.
const imageTokens = 1600

// minOpaque is the shortest string that may be an opaque base64 payload.
// Shorter strings (identifiers, hashes) are ordinary text.
const minOpaque = 256

// Estimate returns the estimated input tokens of raw, a complete request body
// for protocol "anthropic", "openai", "responses", "bedrock" or
// "bedrock-converse". JSON keys and every string, number and literal value
// count as text; structural punctuation is free. Invalid JSON is costed as
// plain text. The result is at least 1 and never depends on map order.
func Estimate(protocol string, raw []byte) int64 {
	cost, opaque, ok := scan(raw)
	if !ok {
		cost = textCost(raw)
	} else if opaque {
		for _, image := range imagePayloads(protocol, raw) {
			charged, _ := valueCost(image)
			cost += 100*imageTokens - charged
		}
	}
	return max(int64(1), (cost+99)/100)
}

// scan walks valid JSON once without building values. It reports whether any
// string value looked like an opaque base64 payload, so the structural image
// pass runs only when it can change the result.
func scan(raw []byte) (cost int64, opaque, ok bool) {
	if !json.Valid(raw) {
		return 0, false, false
	}
	var stack []byte // 'o' object, 'a' array
	key := false
	for i := 0; i < len(raw); {
		switch c := raw[i]; c {
		case '{':
			stack = append(stack, 'o')
			key = true
			i++
		case '[':
			stack = append(stack, 'a')
			key = false
			i++
		case '}', ']':
			stack = stack[:len(stack)-1]
			i++
		case ',':
			key = len(stack) > 0 && stack[len(stack)-1] == 'o'
			i++
		case ':':
			key = false
			i++
		case ' ', '\t', '\n', '\r':
			i++
		case '"':
			end := stringEnd(raw, i)
			if key {
				cost += 100 + decodedCost(raw[i+1:end])
			} else {
				charged, isOpaque := valueCost(raw[i : end+1])
				cost += charged
				opaque = opaque || isOpaque
			}
			i = end + 1
		default:
			end := i
			for end < len(raw) && !delimiter(raw[end]) {
				end++
			}
			cost += textCost(raw[i:end])
			i = end
		}
	}
	return cost, opaque, true
}

func delimiter(c byte) bool {
	switch c {
	case ',', '}', ']', ':', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// stringEnd returns the index of the closing quote of the string at raw[start].
func stringEnd(raw []byte, start int) int {
	for i := start + 1; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return len(raw) - 1
}

// valueCost charges one quoted string value. Opaque base64 (and base64 data
// URLs) keep four bytes per token: decoding and tokenizing them as text would
// overstate them, and only images have a known token ceiling.
func valueCost(quoted []byte) (int64, bool) {
	content := quoted[1 : len(quoted)-1]
	if opaqueBase64(content) {
		return 100 * ceilDiv(len(content), 4), true
	}
	return decodedCost(content), false
}

func opaqueBase64(content []byte) bool {
	if len(content) < minOpaque {
		return false
	}
	if len(content) > 5 && string(content[:5]) == "data:" {
		limit := min(len(content), 128)
		for i := 5; i+8 <= limit; i++ {
			if string(content[i:i+8]) == ";base64," {
				return base64Alphabet(content[i+8:])
			}
		}
		return false
	}
	return base64Alphabet(content)
}

func base64Alphabet(b []byte) bool {
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/', c == '=':
		case c == '\\' && i+1 < len(b) && b[i+1] == '/':
			i++
		default:
			return false
		}
	}
	return true
}

// decodedCost costs the decoded text of a JSON string's raw content without
// allocating: escapes are interpreted in place.
func decodedCost(content []byte) int64 {
	var c counter
	for i := 0; i < len(content); {
		if b := content[i]; b < utf8.RuneSelf && (b != '\\' || i+1 >= len(content)) {
			c.add(rune(b))
			i++
			continue
		} else if b != '\\' {
			r, size := utf8.DecodeRune(content[i:])
			c.add(r)
			i += size
			continue
		}
		switch e := content[i+1]; e {
		case 'n', 'r':
			c.add('\n')
			i += 2
		case 't', 'b', 'f':
			c.add('\t')
			i += 2
		case 'u':
			r, size := unicodeEscape(content[i:])
			c.add(r)
			i += size
		default: // \" \\ \/
			c.add(rune(e))
			i += 2
		}
	}
	return c.total()
}

// unicodeEscape decodes \uXXXX (and a following low surrogate) at b[0].
func unicodeEscape(b []byte) (rune, int) {
	r, ok := hex4(b)
	if !ok {
		return utf8.RuneError, 2
	}
	if r >= 0xD800 && r < 0xDC00 && len(b) >= 12 && b[6] == '\\' && b[7] == 'u' {
		if low, ok := hex4(b[6:]); ok && low >= 0xDC00 && low < 0xE000 {
			return 0x10000 + (r-0xD800)<<10 + (low - 0xDC00), 12
		}
	}
	return r, 6
}

func hex4(b []byte) (rune, bool) {
	if len(b) < 6 {
		return 0, false
	}
	var r rune
	for _, c := range b[2:6] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}
