package classifier

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fake is a classifier whose windows and scores the test controls. Without a
// windows function every segment is its own window.
type fake struct {
	mu      sync.Mutex
	calls   [][]string
	windows func([]string) []string
	score   func(context.Context, []string) (Scores, error)
}

func (f *fake) Windows(s []string) []string {
	if f.windows != nil {
		return f.windows(s)
	}
	return s
}

func (f *fake) Score(ctx context.Context, windows []string) (Scores, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(windows))
	score := f.score
	f.mu.Unlock()
	return score(ctx, windows)
}

// setScore swaps the answer under the mutex: an abandoned Score from an
// earlier deadline test may still be running.
func (f *fake) setScore(score func(context.Context, []string) (Scores, error)) {
	f.mu.Lock()
	f.score = score
	f.mu.Unlock()
}

func (f *fake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func all(v Verdict) func(context.Context, []string) (Scores, error) {
	return func(_ context.Context, windows []string) (Scores, error) {
		out := make([]Verdict, len(windows))
		for i := range out {
			out[i] = v
		}
		return Scores{Verdicts: out, Sent: len(windows)}, nil
	}
}

var allClear = all(Clear)

func testSet(t *testing.T, cache int, entries ...Entry) *Set {
	t.Helper()
	s, err := NewSet(entries, 2*time.Second, 1<<20, cache, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func enforce(name string, c Classifier) Entry {
	return Entry{Name: name, Mode: Enforce, Classifier: c, Global: true}
}

func req(team string, segments ...string) Request {
	return Request{Team: team, Policies: []PolicyRef{{Name: "privacy", Generation: 7, Rule: "protect"}}, Segments: segments}
}

func TestRunSecondIdenticalRequestIsServedFromCache(t *testing.T) {
	f := &fake{score: allClear}
	s := testSet(t, 100, enforce("laya", f))
	first := s.Run(context.Background(), s.Entries, req("kr", "system prompt", "hello"))
	second := s.Run(context.Background(), s.Entries, req("kr", "system prompt", "hello"))
	if first[0].Outcome != OutcomeClear || second[0].Outcome != OutcomeClear || f.callCount() != 1 {
		t.Fatalf("first=%+v second=%+v calls=%d", first, second, f.callCount())
	}
	if second[0].CacheHits != 2 || second[0].CacheMisses != 0 {
		t.Fatalf("cache accounting %+v", second[0])
	}
}

func TestRunNeverCachesFlaggedAbstainOrError(t *testing.T) {
	for name, score := range map[string]func(context.Context, []string) (Scores, error){
		"flagged": all(Flagged),
		"abstain": all(Abstain),
		"error":   func(context.Context, []string) (Scores, error) { return Scores{}, ErrStatus },
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{score: score}
			s := testSet(t, 100, enforce("laya", f))
			s.Run(context.Background(), s.Entries, req("kr", "김민수 010-2345-6789"))
			s.Run(context.Background(), s.Entries, req("kr", "김민수 010-2345-6789"))
			if f.callCount() != 2 {
				t.Fatalf("a %s window was cached", name)
			}
		})
	}
}

func TestRunCachesOnlyClearWindows(t *testing.T) {
	f := &fake{score: func(context.Context, []string) (Scores, error) {
		return Scores{Verdicts: []Verdict{Clear, Abstain}, Sent: 1}, nil
	}}
	s := testSet(t, 100, enforce("laya", f))
	s.Run(context.Background(), s.Entries, req("kr", "read", "unread"))
	f.setScore(allClear)
	s.Run(context.Background(), s.Entries, req("kr", "read", "unread"))
	if got := f.calls[1]; !slices.Equal(got, []string{"unread"}) {
		t.Fatalf("second call sent %q", got)
	}
}

// Review Focus 1: a long tool result is read across requests, never abstaining forever.
func TestRunWarmsAcrossRequestsUntilEverythingIsRead(t *testing.T) {
	const perCall = 2
	f := &fake{
		windows: func(segments []string) []string { // split each segment into 3-rune windows
			var out []string
			for _, s := range segments {
				r := []rune(s)
				for len(r) > 3 {
					out, r = append(out, string(r[:3])), r[3:]
				}
				out = append(out, string(r))
			}
			return out
		},
		score: func(_ context.Context, windows []string) (Scores, error) {
			out := make([]Verdict, len(windows))
			for i := range out {
				if i >= perCall {
					out[i] = Abstain // past the classifier's cap: not read
				}
			}
			return Scores{Verdicts: out, Sent: min(len(windows), perCall)}, nil
		},
	}
	s := testSet(t, 100, enforce("laya", f))
	long := "가나다라마바사아자차카타파하거" // five distinct 3-rune windows
	var outcomes []Outcome
	for range 4 {
		outcomes = append(outcomes, s.Run(context.Background(), s.Entries, req("kr", long))[0].Outcome)
	}
	if !slices.Equal(outcomes, []Outcome{OutcomeAbstain, OutcomeAbstain, OutcomeClear, OutcomeClear}) || f.callCount() != 3 {
		t.Fatalf("outcomes %v, calls %d", outcomes, f.callCount())
	}
}

// Security regression (2026-10-16 commit review): parts cleared separately
// must never clear their combination. The cache holds whole windows only.
func TestRunCannotBeBypassedByCachingPartsSeparately(t *testing.T) {
	f := &fake{
		windows: func(segments []string) []string { return []string{strings.Join(segments, "\n\n")} },
		score: func(_ context.Context, windows []string) (Scores, error) {
			out := make([]Verdict, len(windows))
			for i, w := range windows {
				if strings.Contains(w, "김민수") && strings.Contains(w, "우울증") {
					out[i] = Flagged
				}
			}
			return Scores{Verdicts: out, Sent: len(windows)}, nil
		},
	}
	s := testSet(t, 100, enforce("laya", f))
	for _, part := range []string{"환자 김민수", "진단: 우울증"} {
		if got := s.Run(context.Background(), s.Entries, req("kr", part)); got[0].Outcome != OutcomeClear {
			t.Fatalf("part %q: %+v", part, got)
		}
	}
	if got := s.Run(context.Background(), s.Entries, req("kr", "환자 김민수", "진단: 우울증")); got[0].Outcome != OutcomeFlagged {
		t.Fatalf("the combination was served from its parts' cache entries: %+v", got)
	}
}

func TestRunCachesOnlyTheClearWindowsOfAFlaggedRequest(t *testing.T) {
	f := &fake{score: func(context.Context, []string) (Scores, error) {
		return Scores{Verdicts: []Verdict{Clear, Flagged}, Sent: 2}, nil
	}}
	s := testSet(t, 100, enforce("laya", f))
	if got := s.Run(context.Background(), s.Entries, req("kr", "clean", "risky")); got[0].Outcome != OutcomeFlagged {
		t.Fatalf("%+v", got)
	}
	f.setScore(allClear)
	s.Run(context.Background(), s.Entries, req("kr", "clean", "risky"))
	if got := f.calls[1]; !slices.Equal(got, []string{"risky"}) {
		t.Fatalf("second call sent %q, want only the flagged window", got)
	}
}

func TestRunFlaggedWinsOverAbstain(t *testing.T) {
	for _, verdicts := range [][]Verdict{{Abstain, Flagged}, {Flagged, Abstain}, {Clear, Flagged, Abstain}} {
		f := &fake{score: func(context.Context, []string) (Scores, error) {
			return Scores{Verdicts: verdicts, Sent: len(verdicts)}, nil
		}}
		s := testSet(t, 100, enforce("laya", f))
		segments := []string{"a", "b", "c"}[:len(verdicts)]
		if got := s.Run(context.Background(), s.Entries, req("kr", segments...)); got[0].Outcome != OutcomeFlagged {
			t.Errorf("verdicts %v: %+v", verdicts, got)
		}
	}
}

func TestRunDeduplicatesIdenticalWindows(t *testing.T) {
	f := &fake{score: allClear}
	s := testSet(t, 100, enforce("laya", f))
	s.Run(context.Background(), s.Entries, req("kr", "role", "user", "role", "hello"))
	if !slices.Equal(f.calls[0], []string{"role", "user", "hello"}) {
		t.Fatalf("sent %q", f.calls[0])
	}
}

func TestRunCacheIsScopedByTeamAndPolicy(t *testing.T) {
	f := &fake{score: allClear}
	s := testSet(t, 100, enforce("laya", f))
	s.Run(context.Background(), s.Entries, req("kr", "hello"))
	s.Run(context.Background(), s.Entries, req("us", "hello"))
	changed := req("kr", "hello")
	changed.Policies = []PolicyRef{{Name: "privacy", Generation: 8, Rule: "protect"}}
	s.Run(context.Background(), s.Entries, changed)
	if f.callCount() != 3 {
		t.Fatalf("calls = %d, want 3 (team and policy generation scope the cache)", f.callCount())
	}
}

func TestRunSkipsEmptyAndAbstainsOverInputCap(t *testing.T) {
	f := &fake{score: allClear}
	s := testSet(t, 100, enforce("laya", f))
	if got := s.Run(context.Background(), s.Entries, req("kr", "", "")); got[0].Outcome != OutcomeSkipped {
		t.Fatalf("empty input: %+v", got)
	}
	s.MaxInputBytes = 4
	if got := s.Run(context.Background(), s.Entries, req("kr", "12345")); got[0].Outcome != OutcomeAbstain {
		t.Fatalf("over cap: %+v", got)
	}
	if f.callCount() != 0 {
		t.Fatal("a skipped or over-cap request reached the classifier")
	}
}

func TestRunDeadlineIsAnErrorAndCachesNothing(t *testing.T) {
	f := &fake{score: func(ctx context.Context, windows []string) (Scores, error) {
		<-ctx.Done()
		return allClear(ctx, windows) // a plugin that ignores the deadline must still not populate the cache
	}}
	s := testSet(t, 100, enforce("laya", f))
	s.Timeout = 20 * time.Millisecond
	if got := s.Run(context.Background(), s.Entries, req("kr", "hello")); got[0].Outcome != OutcomeError {
		t.Fatalf("deadline: %+v", got)
	}
	f.setScore(allClear)
	s.Timeout = time.Second
	s.Run(context.Background(), s.Entries, req("kr", "hello"))
	if f.callCount() != 2 {
		t.Fatal("a late answer populated the cache")
	}
}

// A classifier that ignores its context must not hold the request past the deadline.
func TestRunReturnsAtTheDeadlineDespiteAStuckClassifier(t *testing.T) {
	var stuck atomic.Bool
	stuck.Store(true)
	release := make(chan struct{})
	f := &fake{score: func(ctx context.Context, windows []string) (Scores, error) {
		if stuck.Load() {
			<-release // ignores ctx entirely
		}
		return allClear(ctx, windows)
	}}
	s := testSet(t, 100, enforce("laya", f))
	s.Timeout = 50 * time.Millisecond
	start := time.Now()
	got := s.Run(context.Background(), s.Entries, req("kr", "hello"))
	if elapsed := time.Since(start); got[0].Outcome != OutcomeError || elapsed > time.Second {
		t.Fatalf("Run waited %v for a stuck classifier: %+v", elapsed, got)
	}
	stuck.Store(false)
	close(release) // the late answer arrives after Run returned and must not be cached
	s.Timeout = time.Second
	if got := s.Run(context.Background(), s.Entries, req("kr", "hello")); got[0].Outcome != OutcomeClear || f.callCount() != 2 {
		t.Fatalf("late answer was cached or the rerun failed: %+v calls=%d", got, f.callCount())
	}
}

func TestRunRejectsMismatchedVerdictsAndUnknownVerdict(t *testing.T) {
	for name, out := range map[string]Scores{
		"short verdicts":  {Verdicts: []Verdict{Clear}},
		"unknown verdict": {Verdicts: []Verdict{Clear, Verdict(42)}},
	} {
		f := &fake{score: func(context.Context, []string) (Scores, error) { return out, nil }}
		s := testSet(t, 100, enforce("laya", f))
		if got := s.Run(context.Background(), s.Entries, req("kr", "a", "b")); got[0].Outcome != OutcomeError {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestRunEntriesInParallel(t *testing.T) {
	var arrived atomic.Int32
	ready := make(chan struct{})
	both := func(ctx context.Context, windows []string) (Scores, error) {
		if arrived.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready: // reached only if both entries are in flight at once
			return allClear(ctx, windows)
		case <-ctx.Done():
			return Scores{}, ErrTimeout
		}
	}
	a, b := &fake{score: both}, &fake{score: both}
	s := testSet(t, 0, enforce("a", a), Entry{Name: "b", Mode: Shadow, Classifier: b, Global: true})
	for _, r := range s.Run(context.Background(), s.Entries, req("kr", "hello")) {
		if r.Outcome != OutcomeClear {
			t.Fatalf("entries did not run in parallel: %+v", r)
		}
	}
}

func TestRunWithoutCacheStillClassifies(t *testing.T) {
	f := &fake{score: allClear}
	s := testSet(t, 0, enforce("laya", f))
	s.Run(context.Background(), s.Entries, req("kr", "hello"))
	s.Run(context.Background(), s.Entries, req("kr", "hello"))
	if f.callCount() != 2 {
		t.Fatal("a disabled cache served a hit")
	}
}

func TestCombineCountsOnlyEnforce(t *testing.T) {
	for _, tc := range []struct {
		results                   []Result
		classified, uninspectable bool
	}{
		{[]Result{{Mode: Shadow, Outcome: OutcomeFlagged}}, false, false},
		{[]Result{{Mode: Enforce, Outcome: OutcomeFlagged}, {Mode: Enforce, Outcome: OutcomeError}}, true, false},
		{[]Result{{Mode: Enforce, Outcome: OutcomeAbstain}}, false, true},
		{[]Result{{Mode: Enforce, Outcome: OutcomeError}, {Mode: Shadow, Outcome: OutcomeFlagged}}, false, true},
		{[]Result{{Mode: Enforce, Outcome: OutcomeClear}, {Mode: Enforce, Outcome: OutcomeSkipped}}, false, false},
	} {
		c, u := Combine(tc.results)
		if c != tc.classified || u != tc.uninspectable {
			t.Errorf("Combine(%+v) = %v %v", tc.results, c, u)
		}
	}
}

func TestFailedMarksEveryEntryError(t *testing.T) {
	got := Failed([]Entry{{Name: "a", Mode: Enforce}, {Name: "b", Mode: Shadow}})
	if len(got) != 2 || got[0].Outcome != OutcomeError || got[1].Outcome != OutcomeError || got[1].Mode != Shadow {
		t.Fatalf("%+v", got)
	}
}
