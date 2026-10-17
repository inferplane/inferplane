package systemone

import "unicode/utf8"

// boundaryEvery sets the expected number of short units per window. A unit
// for which the classifier's keyed boundary hash is divisible by it can end
// its window, so window edges tend to depend on content, not position: after
// content is inserted earlier in a request, the windows usually fall back
// into step at a later boundary unit, and the windows after that point keep
// their cache entries. That is likely, not guaranteed. The hash is seeded
// randomly per classifier instance (one per generation), so nobody can
// compute boundary units offline.
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
// Appending text leaves every window but the last identical. Text inserted
// earlier changes the windows from the insertion on until a cut falls on the
// same boundary or unit end again. That resynchronisation is probabilistic:
// at the default 1000/150, one insertion changed about two windows on
// average, but when overlap is size/2 a window must hold about size runes
// before a boundary or unit end can end it, so with uniform short units an
// insertion then changes most later windows.
//
// A window ends at a boundary or a unit only after taking in at least overlap
// new runes, and at a newline or size only after four fifths of size, plus
// one, minus overlap, so the windows' total text stays within about three
// times the request text. The result depends only on the arguments.
//
// Arguments outside the option bounds (size < 1, or overlap outside 0 to
// size/2) would stop the cut from advancing; they give the whole text as one
// window instead, so nothing loops and no text is dropped.
func buildWindows(units []string, size, overlap int, boundary func(string) bool) []string {
	type unitEnd struct {
		at       int // rune offset just past the unit in the joined text
		boundary bool
	}
	runes, count := 0, 0
	for _, u := range units {
		if u != "" {
			runes += utf8.RuneCountInString(u)
			count++
		}
	}
	if count == 0 {
		return nil
	}
	text := make([]rune, 0, runes+2*(count-1))
	ends := make([]unitEnd, 0, count)
	for _, u := range units {
		if u == "" {
			continue
		}
		if len(text) > 0 {
			text = append(text, '\n', '\n')
		}
		for _, r := range u {
			text = append(text, r)
		}
		ends = append(ends, unitEnd{at: len(text), boundary: boundary(u)})
	}
	if size < 1 || overlap < 0 || overlap > size/2 {
		return []string{string(text)}
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
		if end < 0 || end-start <= overlap { // defensive: the 2*overlap filter above already ensures progress
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
