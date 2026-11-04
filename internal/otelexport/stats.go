package otelexport

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/inferplane/inferplane/internal/metrics"
)

// Outcomes the observability console adds to
// inferplane_otel_export_records_total (the delivery outcomes are in
// pipeline.go).
const (
	outcomeWithheld   = "withheld_limit" // a runtime limit withheld a policy selection
	outcomeSampledOut = "sampled_out"    // a trace target's sampling draw skipped the span
)

// Decision values of inferplane_otel_export_decisions_total: what governed
// export did with one audit record, one egress request or one span.
const (
	DecisionSelected = "selected"         // at least one exporter receives it
	DecisionNoRule   = "no_rule"          // no observability rule matched the subject
	DecisionConflict = "content_conflict" // matching rules named different content exporters
	DecisionWithheld = "withheld_limit"   // the policy selected exporters; runtime limits withheld all
)

var (
	allOutcomes  = []string{outcomeSent, outcomeQueueFull, outcomeOversize, outcomeExportError, outcomeWithheld, outcomeSampledOut}
	allDecisions = []string{DecisionSelected, DecisionNoRule, DecisionConflict, DecisionWithheld}
	allSignals   = []string{SignalTraces, SignalAudit, SignalContent}
)

// stats is the console's per-process view of export activity. Every count
// mirrors a Prometheus series; the last success and error times and the fixed
// error text exist only here. Keys are bounded by configuration (declared
// exporter names and the legacy "otel"), the three signals and the fixed
// outcome and decision sets, so the maps cannot grow with traffic.
type stats struct {
	m      *metrics.Metrics
	since  time.Time
	counts sync.Map // countKey → *atomic.Uint64
	last   sync.Map // lastKey → *lastState
	decide sync.Map // decideKey → *atomic.Uint64
}

type countKey struct{ exporter, signal, outcome string }

type lastKey struct{ exporter, signal string }

type decideKey struct{ signal, decision string }

type lastState struct {
	success atomic.Int64 // unix nanos of the last delivered batch
	failure atomic.Int64 // unix nanos of the last failed batch
	message atomic.Pointer[string]
}

func newStats(m *metrics.Metrics) *stats { return &stats{m: m, since: time.Now()} }

// loadCounter returns the counter for key, creating it on first use. The
// Load first keeps the request path allocation-free once a key exists.
func loadCounter(m *sync.Map, key any) *atomic.Uint64 {
	if v, ok := m.Load(key); ok {
		return v.(*atomic.Uint64)
	}
	v, _ := m.LoadOrStore(key, new(atomic.Uint64))
	return v.(*atomic.Uint64)
}

// count adds n records for one exporter, signal and outcome.
func (st *stats) count(exporter, signal, outcome string, n int) {
	if n <= 0 {
		return
	}
	st.m.ObserveOTelExport(exporter, signal, outcome, n)
	loadCounter(&st.counts, countKey{exporter, signal, outcome}).Add(uint64(n))
}

// decision counts one governed decision for signal.
func (st *stats) decision(signal, decision string) {
	st.m.ObserveOTelDecision(signal, decision)
	loadCounter(&st.decide, decideKey{signal, decision}).Add(1)
}

func (st *stats) lastOf(exporter, signal string) *lastState {
	key := lastKey{exporter, signal}
	if v, ok := st.last.Load(key); ok {
		return v.(*lastState)
	}
	v, _ := st.last.LoadOrStore(key, new(lastState))
	return v.(*lastState)
}

// delivered stamps a successful batch.
func (st *stats) delivered(exporter, signal string) {
	st.lastOf(exporter, signal).success.Store(time.Now().UnixNano())
}

// failed stamps a failed batch. message must be a fixed string or a status
// code (the client.go errors), never collector content.
func (st *stats) failed(exporter, signal, message string) {
	l := st.lastOf(exporter, signal)
	l.message.Store(&message)
	l.failure.Store(time.Now().UnixNano())
}

// SignalStats is one exporter × signal row of the console: records per
// outcome since the process started and the last delivery result.
type SignalStats struct {
	Signal        string            `json:"signal"`
	Outcomes      map[string]uint64 `json:"outcomes"`
	LastSuccessAt string            `json:"last_success_at,omitempty"`
	LastErrorAt   string            `json:"last_error_at,omitempty"`
	LastError     string            `json:"last_error,omitempty"`
}

// DecisionStats counts governed decisions for one signal.
type DecisionStats struct {
	Signal    string            `json:"signal"`
	Decisions map[string]uint64 `json:"decisions"`
}

func (st *stats) signalStats(exporter, signal string) SignalStats {
	out := SignalStats{Signal: signal, Outcomes: make(map[string]uint64, len(allOutcomes))}
	for _, o := range allOutcomes {
		out.Outcomes[o] = 0
		if v, ok := st.counts.Load(countKey{exporter, signal, o}); ok {
			out.Outcomes[o] = v.(*atomic.Uint64).Load()
		}
	}
	if v, ok := st.last.Load(lastKey{exporter, signal}); ok {
		l := v.(*lastState)
		if ns := l.success.Load(); ns > 0 {
			out.LastSuccessAt = stamp(ns)
		}
		if ns := l.failure.Load(); ns > 0 {
			out.LastErrorAt = stamp(ns)
			if msg := l.message.Load(); msg != nil {
				out.LastError = *msg
			}
		}
	}
	return out
}

func (st *stats) decisions() []DecisionStats {
	out := make([]DecisionStats, 0, len(allSignals))
	for _, sig := range allSignals {
		d := DecisionStats{Signal: sig, Decisions: make(map[string]uint64, len(allDecisions))}
		for _, k := range allDecisions {
			d.Decisions[k] = 0
			if v, ok := st.decide.Load(decideKey{sig, k}); ok {
				d.Decisions[k] = v.(*atomic.Uint64).Load()
			}
		}
		out = append(out, d)
	}
	return out
}

func stamp(ns int64) string { return time.Unix(0, ns).UTC().Format(time.RFC3339) }

// SignalStats reports one exporter × signal row. exporter may be the legacy
// "otel" name, whose span counts arrive through ObserveTrace. A nil Set
// reports an empty row.
func (s *Set) SignalStats(exporter, signal string) SignalStats {
	if s == nil {
		return SignalStats{Signal: signal, Outcomes: map[string]uint64{}}
	}
	return s.st.signalStats(exporter, signal)
}

// ObserveTrace records span export activity that internal/tracing reports for
// one exporter: "sent", "dropped_export_error" or "sampled_out". Any other
// outcome is ignored, so the label set stays fixed. nil-safe.
func (s *Set) ObserveTrace(exporter, outcome string, n int) {
	if s == nil {
		return
	}
	switch outcome {
	case outcomeSent:
		s.st.count(exporter, SignalTraces, outcome, n)
		s.st.delivered(exporter, SignalTraces)
	case outcomeExportError:
		s.st.count(exporter, SignalTraces, outcome, n)
		s.st.failed(exporter, SignalTraces, "span export failed")
	case outcomeSampledOut:
		s.st.count(exporter, SignalTraces, outcome, n)
	}
}
