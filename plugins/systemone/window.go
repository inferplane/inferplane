package systemone

// boundaryEvery sets the expected number of short units per window. A unit
// for which the classifier's keyed boundary hash is divisible by it ends its
// window, so window edges depend on content, not position: content inserted
// earlier in a request changes the windows only up to the next boundary, and
// later windows, with their cache entries, stay identical as a conversation
// grows. The hash is seeded randomly per classifier instance (one per
// generation), so nobody can compute boundary units offline.
const boundaryEvery = 16

// buildWindows cuts the request text, its non-empty units joined by a blank
// line, into windows of at most size runes. Consecutive windows share exactly
// overlap runes, so every stretch of at most overlap runes of that text lies
// wholly inside some window: two parts that close together are always read
// together, whatever the unit sizes and wherever the edges fall.
//
// A window ends, in order of preference:
//   - at the end of the first unit for which boundary reports true, once the
//     window holds at least 2*overlap runes, so it took in at least as much new
//     text as it repeats from the previous window;
//   - otherwise at the last unit end that fits, if the window then holds at
//     least 2*overlap runes;
//   - otherwise at the last newline in its final fifth;
//   - otherwise at size runes.
//
// Boundary edges depend on content, not position, so windows after an edge
// stay identical when text is inserted before it, and every window but the
// last stays identical when text is appended. A window ends at a boundary or
// a unit only after taking in at least overlap new runes, and at a newline or
// size only after at least a fifth of size minus overlap, so the windows'
// total text stays within about three times the request text. The result
// depends only on the arguments.
func buildWindows(units []string, size, overlap int, boundary func(string) bool) []string {
	type unitEnd struct {
		at       int // rune offset just past the unit in the joined text
		boundary bool
	}
	var text []rune
	var ends []unitEnd
	for _, u := range units {
		if u == "" {
			continue
		}
		if len(text) > 0 {
			text = append(text, '\n', '\n')
		}
		text = append(text, []rune(u)...)
		ends = append(ends, unitEnd{at: len(text), boundary: boundary(u)})
	}
	var out []string
	next := 0 // index of the first unit end after start
	for start := 0; start < len(text); {
		limit := start + size
		if limit >= len(text) {
			out = append(out, string(text[start:]))
			break
		}
		for next < len(ends) && ends[next].at <= start {
			next++
		}
		end, lastFit := -1, -1
		for k := next; k < len(ends) && ends[k].at <= limit; k++ {
			if ends[k].at-start < 2*overlap {
				continue
			}
			if ends[k].boundary {
				end = ends[k].at
				break
			}
			lastFit = ends[k].at
		}
		if end < 0 {
			end = lastFit
		}
		if end < 0 || end-start <= overlap {
			end = limit
			for j := limit - 1; j >= start+size*4/5; j-- {
				if text[j] == '\n' {
					end = j + 1
					break
				}
			}
		}
		out = append(out, string(text[start:end]))
		start = end - overlap
	}
	return out
}
