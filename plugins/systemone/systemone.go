// Package systemone is the `systemone` PII classifier. It asks a self-hosted
// /v1/systemone server (Laya, strands-decider) one noul question per window of
// request text and maps the probabilities to a Clear, Flagged or Abstain
// verdict. It never edits a request, never retains text after a call and never
// puts request text, response bodies or the bearer token into an error.
package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/inferplane/inferplane/internal/classifier"
)

const (
	defaultInstructions = "Does this text contain personal information about a specific person, or a secret credential?"
	questionID          = "pii"
	maxResponseBytes    = 256 << 10
)

func init() { classifier.Register("systemone", New) }

var modelPattern = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

type band struct{ low, high float64 }

type settings struct {
	instructions string
	criteria     map[string]string // nil when not configured
	model        string
	batch        bool
	concurrency  int
	windowRunes  int
	overlapRunes int
	maxWindows   int
	threshold    float64
	band         *band
}

type systemone struct {
	endpoint string
	token    string
	client   *http.Client
	s        settings
}

// New validates the threshold and options and builds the classifier. The
// endpoint and token ref are validated by the config layer before New is
// called; the threshold is checked again here so a direct caller cannot
// build a classifier that fails open.
func New(cfg classifier.Config) (classifier.Classifier, error) {
	if !(cfg.Threshold > 0 && cfg.Threshold < 1) {
		return nil, fmt.Errorf("systemone %q: threshold must be in (0,1)", cfg.Name)
	}
	s, err := parseOptions(cfg.Options, cfg.Threshold)
	if err != nil {
		return nil, fmt.Errorf("systemone %q: %w", cfg.Name, err)
	}
	tr := &http.Transport{}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = base.Clone()
	}
	tr.Proxy = nil // request text never leaves through an environment proxy
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &systemone{endpoint: strings.TrimRight(cfg.Endpoint, "/"), token: cfg.BearerToken, client: client, s: s}, nil
}

func parseOptions(raw map[string]json.RawMessage, threshold float64) (settings, error) {
	s := settings{instructions: defaultInstructions, batch: true, concurrency: 4, windowRunes: 1000, overlapRunes: 150, maxWindows: 64, threshold: threshold}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	overlapSet := false
	for _, key := range keys {
		v := raw[key]
		var err error
		switch key {
		case "instructions":
			if err = decodeStrict(v, &s.instructions); err == nil && !runesIn(s.instructions, 1, 512) {
				err = errors.New("must be 1-512 characters")
			}
		case "criteria":
			var c struct {
				True  *string `json:"true"`
				False *string `json:"false"`
			}
			if err = decodeStrict(v, &c); err == nil {
				if c.True == nil || c.False == nil || !runesIn(*c.True, 1, 512) || !runesIn(*c.False, 1, 512) {
					err = errors.New(`needs "true" and "false" strings of 1-512 characters`)
				} else {
					s.criteria = map[string]string{"true": *c.True, "false": *c.False}
				}
			}
		case "model":
			if err = decodeStrict(v, &s.model); err == nil && !modelPattern.MatchString(s.model) {
				err = errors.New("must match [a-z0-9._-]{1,64}")
			}
		case "batch":
			err = decodeStrict(v, &s.batch)
		case "concurrency":
			err = intIn(v, &s.concurrency, 1, 16)
		case "window_runes":
			err = intIn(v, &s.windowRunes, 200, 4000)
		case "window_overlap_runes":
			err = intIn(v, &s.overlapRunes, 0, 2000)
			overlapSet = true
		case "max_windows":
			err = intIn(v, &s.maxWindows, 1, 64)
		case "abstain_band":
			var b struct {
				Low  *float64 `json:"low"`
				High *float64 `json:"high"`
			}
			if err = decodeStrict(v, &b); err == nil {
				if b.Low == nil || b.High == nil || !(0 < *b.Low && *b.Low < threshold && threshold < *b.High && *b.High < 1) {
					err = errors.New("needs 0 < low < threshold < high < 1")
				} else {
					s.band = &band{low: *b.Low, high: *b.High}
				}
			}
		default:
			return settings{}, fmt.Errorf("unknown option %q", key)
		}
		if err != nil {
			return settings{}, fmt.Errorf("option %s: %w", key, err)
		}
	}
	if !overlapSet && s.overlapRunes > s.windowRunes/2 {
		s.overlapRunes = s.windowRunes / 2 // the default never conflicts with a small window
	}
	if s.overlapRunes > s.windowRunes/2 {
		return settings{}, errors.New("option window_overlap_runes: must be at most window_runes/2")
	}
	return s, nil
}

// decodeStrict rejects null, unknown fields and trailing data. Decoder errors
// are replaced because they can quote the value.
func decodeStrict(raw json.RawMessage, into any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("must not be null")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return errors.New("invalid value")
	}
	if d.More() {
		return errors.New("invalid value")
	}
	return nil
}

func intIn(raw json.RawMessage, into *int, low, high int) error {
	if err := decodeStrict(raw, into); err != nil {
		return err
	}
	if *into < low || *into > high {
		return fmt.Errorf("must be %d-%d", low, high)
	}
	return nil
}

func runesIn(s string, low, high int) bool {
	n := utf8.RuneCountInString(s)
	return n >= low && n <= high
}

// Windows packs and splits segments into the deterministic windows this
// classifier reads and the verdict cache stores.
func (c *systemone) Windows(segments []string) []string {
	return buildWindows(segments, c.s.windowRunes, c.s.overlapRunes)
}

// Score reads at most maxWindows windows. Windows past the cap are Abstain
// and are not sent, so a request with unread content is never Clear (PR-08);
// a later request reads them once the read ones are cached.
func (c *systemone) Score(ctx context.Context, windows []string) (classifier.Scores, error) {
	read := windows
	if len(read) > c.s.maxWindows {
		read = windows[:c.s.maxWindows]
	}
	verdicts := make([]classifier.Verdict, len(windows))
	for i := range verdicts {
		verdicts[i] = classifier.Abstain
	}
	if len(read) == 0 {
		return classifier.Scores{Verdicts: verdicts}, nil
	}
	probs, err := c.score(ctx, read)
	if err != nil {
		return classifier.Scores{Sent: len(read)}, err
	}
	for i, p := range probs {
		verdicts[i] = c.judge(p)
	}
	return classifier.Scores{Verdicts: verdicts, Sent: len(read)}, nil
}

func (c *systemone) judge(p float64) classifier.Verdict {
	if c.s.band != nil && c.s.band.low < p && p < c.s.band.high {
		return classifier.Abstain
	}
	if p >= c.s.threshold {
		return classifier.Flagged
	}
	return classifier.Clear
}
