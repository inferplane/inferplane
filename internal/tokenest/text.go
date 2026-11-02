package tokenest

import (
	"unicode"
	"unicode/utf8"
)

// Costs are centitokens (1/100 token) so that fractional per-character rates
// stay integer; Estimate rounds the request total up once. The run model and
// rates are calibrated against the o200k_base tokenizer and the 2026-10-27
// gateway measurements (see estimate_test.go).
const (
	letterRun  = 8   // letter weight per token in one word piece (non-ASCII letters weigh 2)
	digitRun   = 3   // digits per token: tokenizers group at most three
	punctRun   = 2   // ASCII punctuation per token
	spaceRun   = 8   // spaces or tabs per token
	breakRun   = 3   // newlines per token
	hangulCost = 80  // centitokens per Hangul syllable or jamo
	cjkCost    = 70  // centitokens per kana or CJK ideograph
	symbolCost = 100 // centitokens per other non-ASCII rune (arrows, emoji)
)

type class uint8

const (
	clsNone class = iota
	clsLetter
	clsDigit
	clsPunct
	clsSpace // spaces and tabs
	clsBreak // newlines
	clsHangul
	clsCJK
	clsSymbol
)

// asciiClass caches classOf for ASCII, the hot path.
var asciiClass = func() (t [utf8.RuneSelf]class) {
	for r := range t {
		t[r] = classOfSlow(rune(r))
	}
	return t
}()

func classOf(r rune) class {
	if r >= 0 && r < utf8.RuneSelf {
		return asciiClass[r]
	}
	return classOfSlow(r)
}

func classOfSlow(r rune) class {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return clsLetter
	case r >= '0' && r <= '9':
		return clsDigit
	case r == ' ' || r == '\t' || r == '\v' || r == '\f':
		return clsSpace
	case r == '\n' || r == '\r':
		return clsBreak
	case r < utf8.RuneSelf:
		return clsPunct
	case r >= 0xAC00 && r <= 0xD7A3, r >= 0x1100 && r <= 0x11FF, r >= 0x3130 && r <= 0x318F:
		return clsHangul
	case r >= 0x3040 && r <= 0x30FF, r >= 0x3400 && r <= 0x4DBF, r >= 0x4E00 && r <= 0x9FFF,
		r >= 0xF900 && r <= 0xFAFF, r >= 0xFF00 && r <= 0xFFEF:
		return clsCJK
	case unicode.IsLetter(r):
		return clsLetter
	default:
		return clsSymbol
	}
}

// counter folds runes into pretoken-like runs the way BPE tokenizers pre-split
// text: a run ends where the character class changes, and a letter run also
// ends where a lowercase letter is followed by an uppercase one (camelCase).
// A run is charged when the next run starts, because tokenizers attach one
// preceding space, tab or single punctuation mark to a following word, while
// a space before digits stays its own token.
type counter struct {
	cls       class
	n         int // runes in the current run
	weight    int // letter weight of the current run
	lastLower bool
	cost      int64
}

func ceilDiv(n, d int) int64 { return int64((n + d - 1) / d) }

func word(next class) bool { return next == clsLetter || next == clsHangul || next == clsCJK }

// charge adds the current run's cost given the class of the run after it.
func (c *counter) charge(next class) {
	switch c.cls {
	case clsLetter:
		c.cost += 100 * ceilDiv(c.weight, letterRun)
	case clsDigit:
		c.cost += 100 * ceilDiv(c.n, digitRun)
	case clsPunct:
		if c.n > 1 || !word(next) {
			c.cost += 100 * ceilDiv(c.n, punctRun)
		}
	case clsSpace:
		switch next {
		case clsNone:
			c.cost += 100 * ceilDiv(c.n, spaceRun)
		case clsDigit:
			c.cost += 100 * (ceilDiv(c.n-1, spaceRun) + 1)
		default:
			c.cost += 100 * ceilDiv(c.n-1, spaceRun)
		}
	case clsBreak:
		c.cost += 100 * ceilDiv(c.n, breakRun)
	case clsHangul:
		c.cost += hangulCost * int64(c.n)
	case clsCJK:
		c.cost += cjkCost * int64(c.n)
	case clsSymbol:
		if c.n > 1 || !word(next) {
			c.cost += symbolCost * int64(c.n)
		}
	}
}

func (c *counter) add(r rune) {
	cls := classOf(r)
	upper := r >= 'A' && r <= 'Z'
	if cls != c.cls || (cls == clsLetter && upper && c.lastLower) {
		c.charge(cls)
		c.cls, c.n, c.weight = cls, 0, 0
	}
	c.n++
	if r < utf8.RuneSelf {
		c.weight++
	} else {
		c.weight += 2
	}
	c.lastLower = r >= 'a' && r <= 'z'
}

func (c *counter) total() int64 {
	c.charge(clsNone)
	c.cls, c.n, c.weight = clsNone, 0, 0
	return c.cost
}

// textCost returns the centitoken cost of decoded text.
func textCost(s []byte) int64 {
	var c counter
	for len(s) > 0 {
		if s[0] < utf8.RuneSelf {
			c.add(rune(s[0]))
			s = s[1:]
			continue
		}
		r, size := utf8.DecodeRune(s)
		c.add(r)
		s = s[size:]
	}
	return c.total()
}
