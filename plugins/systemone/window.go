package systemone

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// boundaryEvery sets the expected number of short units per window. A unit
// for which the classifier's keyed boundary hash is divisible by it ends its
// window, so window edges depend on content, not position: content inserted
// earlier in a request changes the windows only up to the next boundary, and
// later windows, with their cache entries, stay identical as a conversation
// grows. The hash is seeded randomly per classifier instance (one per
// generation), so nobody can compute boundary units offline.
const boundaryEvery = 16

// buildWindows packs consecutive short units into windows of at most size
// runes, joined by a blank line; ends a window after a unit for which
// boundary reports true; and splits a unit longer than size with splitRunes.
// Each packed window starts with the trailing units of the previous packed
// window: always its last unit, plus earlier ones while the carried text
// stays within overlap runes, dropping the oldest until the new unit fits.
// So two short units with at most overlap runes of text from the first to
// the second are read together in some window, and no edge can separate them.
// Empty units get no window. The result depends only on the arguments.
func buildWindows(units []string, size, overlap int, boundary func(string) bool) []string {
	var out []string
	var win, carry []string // units of the open window; trailing units carried into the next
	runes := 0              // runes of the open window, separators included
	flush := func(keep bool) {
		if len(win) > 0 {
			out = append(out, strings.Join(win, "\n\n"))
			if keep {
				carry = tail(win, overlap)
			}
		}
		if !keep {
			carry = nil
		}
		win, runes = nil, 0
	}
	for _, u := range units {
		n := utf8.RuneCountInString(u)
		switch {
		case n == 0:
			continue
		case n > size:
			flush(false)
			out = append(out, splitRunes(u, size, overlap)...)
			continue
		}
		if len(win) > 0 && runes+2+n > size {
			flush(true)
		}
		if len(win) == 0 {
			for len(carry) > 0 && joinedRunes(carry)+2+n > size {
				carry = carry[1:] // drop the oldest carried unit until u fits
			}
			win, runes, carry = carry, joinedRunes(carry), nil
		}
		if len(win) > 0 {
			runes += 2
		}
		win = append(win, u)
		runes += n
		if boundary(u) {
			flush(true)
		}
	}
	flush(false)
	return out
}

// tail returns the last unit plus earlier units while the carried text,
// separators included, stays within overlap runes.
func tail(units []string, overlap int) []string {
	i := len(units) - 1
	total := utf8.RuneCountInString(units[i])
	for i > 0 {
		next := total + 2 + utf8.RuneCountInString(units[i-1])
		if next > overlap {
			break
		}
		total, i = next, i-1
	}
	return slices.Clone(units[i:])
}

func joinedRunes(units []string) int {
	n := 0
	for i, u := range units {
		if i > 0 {
			n += 2
		}
		n += utf8.RuneCountInString(u)
	}
	return n
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
