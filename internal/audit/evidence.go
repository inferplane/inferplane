package audit

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Attempt statuses are a closed set (ADR-057).
const (
	AttemptOK            = "ok"
	AttemptUpstreamError = "upstream_error"
	AttemptModelNotFound = "model_not_found"
	AttemptStreamFailed  = "stream_failed"
	AttemptTimeout       = "timeout"
	AttemptCanceled      = "canceled"
)

// maxAttempts bounds attempts[] on one record: the first seven and the last.
const maxAttempts = 8

// AttemptRef is one provider call: the configured provider name (never a
// URL), the model, a closed status, the upstream HTTP status when known, and
// offsets from the request start.
type AttemptRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Status   string `json:"status"`
	HTTP     int    `json:"http,omitempty"`
	StartMs  int64  `json:"start_ms"`
	DurMs    int64  `json:"dur_ms"`
}

// ClientRef is bounded client context: an allow-listed request class and a
// key-scoped session digest.
type ClientRef struct {
	RequestClass string `json:"request_class,omitempty"`
	SessionRef   string `json:"session_ref,omitempty"`
}

// AutoModeRef is an estimated Claude Code auto mode classifier verdict.
// Stage is one of s1, s2 or server.
type AutoModeRef struct {
	Verdict   string `json:"verdict"`
	Stage     string `json:"stage,omitempty"`
	Signature string `json:"signature"`
}

type evidence struct {
	mu       sync.Mutex
	start    time.Time
	client   *ClientRef
	ttft     time.Duration
	attempts []AttemptRef
	open     int
	auto     *AutoModeRef
}

type evidenceKey struct{}

// WithEvidence attaches one request's evidence collector. The data-plane
// middleware calls it once per request after authentication.
func WithEvidence(ctx context.Context, start time.Time, client *ClientRef) context.Context {
	e := &evidence{start: start, open: -1}
	if client != nil {
		c := *client
		e.client = &c
	}
	return context.WithValue(ctx, evidenceKey{}, e)
}

func evidenceFrom(ctx context.Context) *evidence {
	e, _ := ctx.Value(evidenceKey{}).(*evidence)
	return e
}

// ClientFrom returns a copy of the request's client evidence, or nil.
func ClientFrom(ctx context.Context) *ClientRef {
	e := evidenceFrom(ctx)
	if e == nil || e.client == nil {
		return nil
	}
	c := *e.client
	return &c
}

func (e *evidence) sinceMs() int64 { return time.Since(e.start).Milliseconds() }

// BeginAttempt records the start of one provider call and returns its end
// function. Ending twice is a no-op; an attempt still open when the terminal
// record is written is closed from that record's outcome.
func BeginAttempt(ctx context.Context, provider, model string) func(status string, httpStatus int) {
	e := evidenceFrom(ctx)
	if e == nil {
		return func(string, int) {}
	}
	e.mu.Lock()
	idx := len(e.attempts)
	e.attempts = append(e.attempts, AttemptRef{Provider: provider, Model: model, StartMs: e.sinceMs()})
	e.open = idx
	e.mu.Unlock()
	return func(status string, httpStatus int) {
		e.mu.Lock()
		e.endLocked(idx, status, httpStatus)
		e.mu.Unlock()
	}
}

func (e *evidence) endLocked(idx int, status string, httpStatus int) {
	a := &e.attempts[idx]
	if a.Status != "" {
		return
	}
	a.Status = status
	if httpStatus > 0 {
		a.HTTP = httpStatus
	}
	a.DurMs = max(e.sinceMs()-a.StartMs, 0)
	if e.open == idx {
		e.open = -1
	}
}

// EndOpenAttempt closes the open attempt now with a closed status and the
// upstream HTTP status (0 when the upstream never answered). Later closes,
// including Finish, leave it unchanged.
func EndOpenAttempt(ctx context.Context, status string, httpStatus int) {
	e := evidenceFrom(ctx)
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.open >= 0 {
		e.endLocked(e.open, status, httpStatus)
	}
	e.mu.Unlock()
}

// NoteAttemptHTTP records the upstream status of the open attempt.
func NoteAttemptHTTP(ctx context.Context, httpStatus int) {
	e := evidenceFrom(ctx)
	if e == nil || httpStatus <= 0 {
		return
	}
	e.mu.Lock()
	if e.open >= 0 {
		e.attempts[e.open].HTTP = httpStatus
	}
	e.mu.Unlock()
}

// OpenAttemptHTTP returns the upstream HTTP status noted on the open
// attempt, or 0 when no attempt is open or none was noted.
func OpenAttemptHTTP(ctx context.Context) int {
	e := evidenceFrom(ctx)
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.open < 0 {
		return 0
	}
	return e.attempts[e.open].HTTP
}

// MarkFirstToken records time to first token once.
func MarkFirstToken(ctx context.Context) {
	e := evidenceFrom(ctx)
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.ttft == 0 {
		e.ttft = max(time.Since(e.start), time.Millisecond)
	}
	e.mu.Unlock()
}

// SetAutoMode stores the request's estimated classifier verdict.
func SetAutoMode(ctx context.Context, ref *AutoModeRef) {
	e := evidenceFrom(ctx)
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if ref == nil {
		e.auto = nil
		return
	}
	c := *ref
	e.auto = &c
}

// Finish stamps the collected evidence onto a terminal record: a
// request_completed record, or a request_started record that carries an
// outcome (how every denial is written). Every other record is unchanged.
func Finish(ctx context.Context, rec *Record) {
	e := evidenceFrom(ctx)
	if e == nil || rec == nil || !terminalRecord(rec) {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil {
		c := *e.client
		rec.Client = &c
	}
	if rec.Latency == nil {
		rec.Latency = &LatencyRef{TotalMs: e.sinceMs(), TTFTMs: e.ttft.Milliseconds()}
	}
	if e.open >= 0 {
		status, code := finalAttemptStatus(ctx, rec.Outcome)
		e.endLocked(e.open, status, code)
	}
	if len(e.attempts) > 0 {
		rec.Attempts = boundedAttempts(e.attempts)
	}
	if len(e.attempts) > 1 && rec.Outcome != nil {
		chain := make([]string, len(rec.Attempts))
		for i, a := range rec.Attempts {
			chain[i] = a.Provider
		}
		rec.Outcome.FallbackUsed = true
		rec.Outcome.FallbackChain = chain
	}
	if rec.Event == "request_completed" && e.auto != nil {
		a := *e.auto
		rec.AutoMode = &a
	}
}

func terminalRecord(r *Record) bool {
	return r.Event == "request_completed" || (r.Event == "request_started" && r.Outcome != nil)
}

func finalAttemptStatus(ctx context.Context, o *OutcomeRef) (string, int) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return AttemptCanceled, 0
	}
	if o == nil {
		return AttemptUpstreamError, 0
	}
	switch {
	case o.Partial:
		return AttemptStreamFailed, o.Status
	case o.Status >= 200 && o.Status < 400:
		return AttemptOK, o.Status
	case o.Status == 504:
		return AttemptTimeout, o.Status
	default:
		return AttemptUpstreamError, o.Status
	}
}

func boundedAttempts(in []AttemptRef) []AttemptRef {
	if len(in) <= maxAttempts {
		return slices.Clone(in)
	}
	out := slices.Clone(in[:maxAttempts-1])
	return append(out, in[len(in)-1])
}
