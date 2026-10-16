package classifier

import (
	"context"
	"time"
)

// Outcome is the bounded audit and metric result of one entry on one request.
type Outcome string

const (
	OutcomeFlagged Outcome = "flagged"
	OutcomeClear   Outcome = "clear"
	OutcomeAbstain Outcome = "abstain"
	OutcomeError   Outcome = "error"
	OutcomeSkipped Outcome = "skipped"
)

// Request is one routing request's classifier input. Segments are borrowed.
type Request struct {
	Team     string
	Policies []PolicyRef
	Segments []string
}

// Result reports one entry's outcome. It carries no text.
type Result struct {
	Name, Revision string
	Mode           Mode
	Outcome        Outcome
	Duration       time.Duration
	Windows        int
	CacheHits      int
	CacheMisses    int
}

// Run classifies req with every entry in parallel under the Set's timeout. It
// never retries and returns by the deadline even if a classifier ignores its
// context; an entry still running at the deadline is an error, and its late
// answer is never cached.
func (s *Set) Run(ctx context.Context, entries []Entry, req Request) []Result {
	results := make([]Result, len(entries))
	for i, e := range entries {
		results[i] = Result{Name: e.Name, Revision: e.Revision, Mode: e.Mode}
	}
	total, empty := 0, true
	for _, t := range req.Segments {
		total += len(t)
		empty = empty && t == ""
	}
	switch {
	case len(entries) == 0:
		return results
	case empty:
		return withOutcome(results, OutcomeSkipped)
	case total > s.MaxInputBytes:
		return withOutcome(results, OutcomeAbstain)
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	start := time.Now()
	done := make([]chan Result, len(entries))
	for i := range entries {
		done[i] = make(chan Result, 1) // buffered: a late answer never blocks its goroutine
		go func(i int, res Result) {
			s.runEntry(ctx, &entries[i], req, &res)
			done[i] <- res
		}(i, results[i])
	}
	for i := range entries {
		select {
		case results[i] = <-done[i]:
			continue
		default:
		}
		select {
		case results[i] = <-done[i]:
		case <-ctx.Done():
			// A classifier that ignores its context must not hold the request
			// past the deadline. Its late answer is discarded and, because
			// runEntry checks ctx.Err() before storing, never cached.
			results[i].Outcome = OutcomeError
			results[i].Duration = time.Since(start)
		}
	}
	return results
}

func (s *Set) runEntry(ctx context.Context, e *Entry, req Request, res *Result) {
	start := time.Now()
	defer func() { res.Duration = time.Since(start) }()
	var pending []string
	var keys [][32]byte
	seen := make(map[string]bool)
	for _, w := range e.Classifier.Windows(req.Segments) {
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		if s.cache != nil {
			k := s.cache.digest(req.Team, e, req.Policies, w)
			if s.cache.clear(k) {
				res.CacheHits++
				continue
			}
			keys = append(keys, k)
		}
		res.CacheMisses++
		pending = append(pending, w)
	}
	if len(pending) == 0 {
		res.Outcome = OutcomeClear
		return
	}
	out, err := e.Classifier.Score(ctx, pending)
	res.Windows = out.Sent
	if err != nil || ctx.Err() != nil || len(out.Verdicts) != len(pending) {
		res.Outcome = OutcomeError
		return
	}
	res.Outcome = OutcomeClear
	for _, v := range out.Verdicts {
		switch v {
		case Clear:
		case Flagged:
			res.Outcome = OutcomeFlagged
		case Abstain:
			if res.Outcome == OutcomeClear {
				res.Outcome = OutcomeAbstain
			}
		default:
			res.Outcome = OutcomeError
			return
		}
	}
	// Clear windows are cached even when another window flagged or abstained:
	// each verdict covers exactly its window's text, so a long request warms
	// the cache window by window and a later request reads only the rest.
	if s.cache != nil {
		for i, v := range out.Verdicts {
			if v == Clear {
				s.cache.store(keys[i])
			}
		}
	}
}

func withOutcome(results []Result, o Outcome) []Result {
	for i := range results {
		results[i].Outcome = o
	}
	return results
}

// Failed reports every entry as an error, for an input that could not be extracted.
func Failed(entries []Entry) []Result {
	results := make([]Result, len(entries))
	for i, e := range entries {
		results[i] = Result{Name: e.Name, Revision: e.Revision, Mode: e.Mode, Outcome: OutcomeError}
	}
	return results
}

// Combine applies the Enforce-only rule: any Enforce flagged classifies the
// request; otherwise any Enforce abstain or error makes it uninspectable.
func Combine(results []Result) (classified, uninspectable bool) {
	for _, r := range results {
		if r.Mode != Enforce {
			continue
		}
		switch r.Outcome {
		case OutcomeFlagged:
			classified = true
		case OutcomeAbstain, OutcomeError:
			uninspectable = true
		}
	}
	if classified {
		uninspectable = false
	}
	return classified, uninspectable
}
