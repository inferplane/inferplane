package systemone

import (
	"strings"
	"unicode/utf8"
)

// window is one /v1/systemone state: text packed from units, and the indexes
// of every unit with any text in it.
type window struct {
	text  string
	parts []int
}

// buildWindows packs consecutive short units into windows of at most size
// runes, joined by a blank line, and splits a unit longer than size with
// splitRunes. Empty units get no window.
func buildWindows(units []string, size, overlap int) []window {
	var out []window
	var cur strings.Builder
	var parts []int
	runes := 0
	flush := func() {
		if len(parts) > 0 {
			out = append(out, window{text: cur.String(), parts: parts})
		}
		cur.Reset()
		parts, runes = nil, 0
	}
	for i, u := range units {
		n := utf8.RuneCountInString(u)
		switch {
		case n == 0:
			continue
		case n > size:
			flush()
			for _, chunk := range splitRunes(u, size, overlap) {
				out = append(out, window{text: chunk, parts: []int{i}})
			}
			continue
		}
		sep := 0
		if len(parts) > 0 {
			sep = 2
		}
		if runes+sep+n > size {
			flush()
			sep = 0
		}
		if sep > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(u)
		parts = append(parts, i)
		runes += sep + n
	}
	flush()
	return out
}

// splitRunes cuts s into windows of at most size runes. Each cut prefers the
// last newline in the final fifth of the window, and each window after the
// first repeats the previous window's last overlap runes. It requires
// 0 <= overlap <= size/2, which the option bounds guarantee.
func splitRunes(s string, size, overlap int) []string {
	r := []rune(s)
	var out []string
	for start := 0; ; {
		end := start + size
		if end >= len(r) {
			return append(out, string(r[start:]))
		}
		for j := end - 1; j >= start+size*4/5; j-- {
			if r[j] == '\n' {
				end = j + 1
				break
			}
		}
		out = append(out, string(r[start:end]))
		next := end - overlap
		if next <= start {
			next = end
		}
		start = next
	}
}
