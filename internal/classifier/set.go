package classifier

import (
	"slices"
	"time"
)

// Entry is one resolved classifier of a generation.
type Entry struct {
	Name, Revision string
	Mode           Mode
	Classifier     Classifier
	Global         bool
	Teams          map[string]bool
	// Settings is SettingsDigest of the entry's Config; part of every cache key.
	Settings [32]byte
}

// Set is the classifier view of one live.State generation, with its own
// verdict cache and HMAC key. A nil Set has no entries.
type Set struct {
	Entries       []Entry
	Timeout       time.Duration
	MaxInputBytes int
	cache         *verdictCache
}

// NewSet builds a Set. cacheEntries == 0 disables the verdict cache.
func NewSet(entries []Entry, timeout time.Duration, maxInputBytes, cacheEntries int, cacheTTL time.Duration) (*Set, error) {
	c, err := newVerdictCache(cacheEntries, cacheTTL, time.Now)
	if err != nil {
		return nil, err
	}
	return &Set{Entries: slices.Clone(entries), Timeout: timeout, MaxInputBytes: maxInputBytes, cache: c}, nil
}

// For returns the entries that apply to team, in configuration order.
func (s *Set) For(team string) []Entry {
	if s == nil {
		return nil
	}
	var out []Entry
	for _, e := range s.Entries {
		if e.Global || e.Teams[team] {
			out = append(out, e)
		}
	}
	return out
}
