package systemone

import (
	"hash/fnv"
	"strings"
	"unicode/utf8"
)

// boundaryEvery sets the expected number of short units per window. A unit
// whose FNV-1a hash is divisible by it ends its window, so window edges
// depend on content, not position: content inserted earlier in a request
// changes the windows only up to the next boundary, and every later window,
// with its cache entry, stays identical as a conversation grows.
const boundaryEvery = 16

func boundary(unit string) bool {
	h := fnv.New32a()
	h.Write([]byte(unit))
	return h.Sum32()%boundaryEvery == 0
}

// buildWindows packs consecutive short units into windows of at most size
// runes, joined by a blank line, ends a window after a boundary unit, and
// splits a unit longer than size with splitRunes. Empty units get no window.
// The result depends only on units, size and overlap.
func buildWindows(units []string, size, overlap int) []string {
	var out []string
	var cur strings.Builder
	runes, open := 0, false
	flush := func() {
		if open {
			out = append(out, cur.String())
		}
		cur.Reset()
		runes, open = 0, false
	}
	for _, u := range units {
		n := utf8.RuneCountInString(u)
		switch {
		case n == 0:
			continue
		case n > size:
			flush()
			out = append(out, splitRunes(u, size, overlap)...)
			continue
		}
		sep := 0
		if open {
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
		runes += sep + n
		open = true
		if boundary(u) {
			flush()
		}
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
