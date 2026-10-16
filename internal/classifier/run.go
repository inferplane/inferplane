package classifier

import (
	"context"
	"sync"
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
// never retries. An entry still running at the deadline is an error, and its
// answer, if it arrives later, is not cached.
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
	var wg sync.WaitGroup
	for i := range entries {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.runEntry(ctx, &entries[i], req, &results[i])
		}(i)
	}
	wg.Wait()
	return results
}

func (s *Set) runEntry(ctx context.Context, e *Entry, req Request, res *Result) {
	start := time.Now()
	defer func() { res.Duration = time.Since(start) }()
	var texts []string
	var keys [][32]byte
	seen := make(map[string]bool)
	for _, unit := range e.Classifier.Units(req.Segments) {
		if unit == "" || seen[unit] {
			continue
		}
		seen[unit] = true
		if s.cache != nil {
			k := s.cache.digest(req.Team, e, req.Policies, unit)
			if s.cache.clear(k) {
				res.CacheHits++
				continue
			}
			keys = append(keys, k)
		}
		res.CacheMisses++
		texts = append(texts, unit)
	}
	if len(texts) == 0 {
		res.Outcome = OutcomeClear
		return
	}
	out, err := e.Classifier.Classify(ctx, Input{Texts: texts})
	res.Windows = out.Windows
	if err != nil || ctx.Err() != nil || (out.Clear != nil && len(out.Clear) != len(texts)) {
		res.Outcome = OutcomeError
		return
	}
	switch out.Verdict {
	case Clear:
		res.Outcome = OutcomeClear
	case Flagged:
		res.Outcome = OutcomeFlagged
	case Abstain:
		res.Outcome = OutcomeAbstain
	default:
		res.Outcome = OutcomeError
		return
	}
	if s.cache != nil {
		for i, ok := range out.Clear {
			if ok {
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
