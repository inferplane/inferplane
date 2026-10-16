package classifier

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fake struct {
	mu     sync.Mutex
	calls  [][]string
	units  func([]string) []string
	answer func(context.Context, Input) (Output, error)
}

func (f *fake) Units(s []string) []string {
	if f.units != nil {
		return f.units(s)
	}
	return s
}

func (f *fake) Classify(ctx context.Context, in Input) (Output, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(in.Texts))
	answer := f.answer
	f.mu.Unlock()
	return answer(ctx, in)
}

// setAnswer swaps the answer under the lock: a classifier that Run abandoned at
// the deadline may still be inside Classify when the test changes it.
func (f *fake) setAnswer(a func(context.Context, Input) (Output, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = a
}

func (f *fake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func allClear(_ context.Context, in Input) (Output, error) {
	ok := make([]bool, len(in.Texts))
	for i := range ok {
		ok[i] = true
	}
	return Output{Verdict: Clear, Clear: ok, Windows: 1}, nil
}

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
	f := &fake{answer: allClear}
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
	for name, answer := range map[string]func(context.Context, Input) (Output, error){
		"flagged": func(context.Context, Input) (Output, error) { return Output{Verdict: Flagged}, nil },
		"abstain": func(context.Context, Input) (Output, error) { return Output{Verdict: Abstain}, nil },
		"error":   func(context.Context, Input) (Output, error) { return Output{}, ErrStatus },
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{answer: answer}
			s := testSet(t, 100, enforce("laya", f))
			s.Run(context.Background(), s.Entries, req("kr", "김민수 010-2345-6789"))
			s.Run(context.Background(), s.Entries, req("kr", "김민수 010-2345-6789"))
			if f.callCount() != 2 {
				t.Fatalf("a %s answer was cached", name)
			}
		})
	}
}

func TestRunCachesOnlyReportedClearUnits(t *testing.T) {
	f := &fake{answer: func(_ context.Context, in Input) (Output, error) {
		return Output{Verdict: Abstain, Clear: []bool{true, false}}, nil
	}}
	s := testSet(t, 100, enforce("laya", f))
	s.Run(context.Background(), s.Entries, req("kr", "read", "unread"))
	f.answer = allClear
	s.Run(context.Background(), s.Entries, req("kr", "read", "unread"))
	if got := f.calls[1]; !slices.Equal(got, []string{"unread"}) {
		t.Fatalf("second call sent %q", got)
	}
}

// Review Focus 1: a long tool result is read across requests, never abstaining forever.
func TestRunWarmsAcrossRequestsUntilEverythingIsRead(t *testing.T) {
	const perCall = 2
	f := &fake{
		units: func(segments []string) []string { // split each segment into 3-rune chunks
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
		answer: func(_ context.Context, in Input) (Output, error) {
			ok := make([]bool, len(in.Texts))
			for i := 0; i < len(ok) && i < perCall; i++ {
				ok[i] = true
			}
			if len(in.Texts) > perCall {
				return Output{Verdict: Abstain, Clear: ok}, nil
			}
			return Output{Verdict: Clear, Clear: ok}, nil
		},
	}
	s := testSet(t, 100, enforce("laya", f))
	long := "가나다라마바사아자차카타파하거" // five distinct 3-rune units
	var outcomes []Outcome
	for range 4 {
		outcomes = append(outcomes, s.Run(context.Background(), s.Entries, req("kr", long))[0].Outcome)
	}
	if !slices.Equal(outcomes, []Outcome{OutcomeAbstain, OutcomeAbstain, OutcomeClear, OutcomeClear}) || f.callCount() != 3 {
		t.Fatalf("outcomes %v, calls %d", outcomes, f.callCount())
	}
}

func TestRunDeduplicatesIdenticalUnits(t *testing.T) {
	f := &fake{answer: allClear}
	s := testSet(t, 100, enforce("laya", f))
	s.Run(context.Background(), s.Entries, req("kr", "role", "user", "role", "hello"))
	if !slices.Equal(f.calls[0], []string{"role", "user", "hello"}) {
		t.Fatalf("sent %q", f.calls[0])
	}
}

func TestRunCacheIsScopedByTeamAndPolicy(t *testing.T) {
	f := &fake{answer: allClear}
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
	f := &fake{answer: allClear}
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
	f := &fake{answer: func(ctx context.Context, in Input) (Output, error) {
		<-ctx.Done()
		return allClear(ctx, in) // a plugin that ignores the deadline must still not populate the cache
	}}
	s := testSet(t, 100, enforce("laya", f))
	s.Timeout = 20 * time.Millisecond
	if got := s.Run(context.Background(), s.Entries, req("kr", "hello")); got[0].Outcome != OutcomeError {
		t.Fatalf("deadline: %+v", got)
	}
	f.setAnswer(allClear)
	s.Timeout = time.Second
	s.Run(context.Background(), s.Entries, req("kr", "hello"))
	if f.callCount() != 2 {
		t.Fatal("a late answer populated the cache")
	}
}

func TestRunRejectsMismatchedClearAndUnknownVerdict(t *testing.T) {
	for name, out := range map[string]Output{
		"short clear":     {Verdict: Clear, Clear: []bool{true}},
		"unknown verdict": {Verdict: Verdict(42)},
	} {
		f := &fake{answer: func(context.Context, Input) (Output, error) { return out, nil }}
		s := testSet(t, 100, enforce("laya", f))
		if got := s.Run(context.Background(), s.Entries, req("kr", "a", "b")); got[0].Outcome != OutcomeError {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestRunEntriesInParallel(t *testing.T) {
	var arrived atomic.Int32
	ready := make(chan struct{})
	both := func(ctx context.Context, in Input) (Output, error) {
		if arrived.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready: // reached only if both entries are in flight at once
			return allClear(ctx, in)
		case <-ctx.Done():
			return Output{}, ErrTimeout
		}
	}
	a, b := &fake{answer: both}, &fake{answer: both}
	s := testSet(t, 0, enforce("a", a), Entry{Name: "b", Mode: Shadow, Classifier: b, Global: true})
	for _, r := range s.Run(context.Background(), s.Entries, req("kr", "hello")) {
		if r.Outcome != OutcomeClear {
			t.Fatalf("entries did not run in parallel: %+v", r)
		}
	}
}

func TestRunWithoutCacheStillClassifies(t *testing.T) {
	f := &fake{answer: allClear}
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

// A classifier that ignores its context must not hold the request past the deadline.
func TestRunReturnsAtTheDeadlineDespiteAStuckClassifier(t *testing.T) {
	var stuck atomic.Bool
	stuck.Store(true)
	release := make(chan struct{})
	f := &fake{answer: func(ctx context.Context, in Input) (Output, error) {
		if stuck.Load() {
			<-release // ignores ctx entirely
		}
		return allClear(ctx, in)
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

func TestRunRejectsAClearVerdictWithUnreadUnits(t *testing.T) {
	f := &fake{answer: func(context.Context, Input) (Output, error) {
		return Output{Verdict: Clear, Clear: []bool{true, false}}, nil
	}}
	s := testSet(t, 100, enforce("laya", f))
	if got := s.Run(context.Background(), s.Entries, req("kr", "a", "b")); got[0].Outcome != OutcomeError {
		t.Fatalf("%+v", got)
	}
}
