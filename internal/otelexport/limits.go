package otelexport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

// Limit narrows one declared exporter at runtime (the observability console,
// sub-project B). It can only take away from the bootstrap ceiling: Paused
// stops the exporter, Signals keeps a subset of its declared signals, and
// MaxContentBytes and MaxSamplePercent only lower. Nothing here names an
// endpoint, protocol, TLS setting or header, so a limit can never redirect a
// record. The zero value narrows nothing.
type Limit struct {
	Paused           bool     `json:"paused,omitempty"`
	Signals          []string `json:"signals,omitempty"`            // nil = every declared signal
	MaxContentBytes  int64    `json:"max_content_bytes,omitempty"`  // 0 = the declared cap
	MaxSamplePercent int      `json:"max_sample_percent,omitempty"` // 0 = 100
}

// Limits is the runtime narrowing, keyed by declared exporter name.
type Limits map[string]Limit

// clone copies l; an empty non-nil signal list stays empty (it means "no
// signal", not "every signal").
func (l Limit) clone() Limit {
	if l.Signals != nil {
		l.Signals = append(make([]string, 0, len(l.Signals)), l.Signals...)
	}
	return l
}

func (l Limits) clone() Limits {
	out := make(Limits, len(l))
	for name, lim := range l {
		out[name] = lim.clone()
	}
	return out
}

func sortedNames(l Limits) []string {
	names := make([]string, 0, len(l))
	for name := range l {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Normalize validates l against the declared ceiling and returns its
// canonical form: signals sorted, a list equal to the declared set dropped,
// values equal to the bootstrap dropped and no-op entries removed, so two
// equivalent edits share one revision. Anything wider than the ceiling is an
// error, never clamped: an edit cannot widen.
func (s *Set) Normalize(l Limits) (Limits, error) {
	out := Limits{}
	if len(l) == 0 {
		return out, nil
	}
	if s == nil {
		return nil, errors.New("no observability exporters are declared on this data plane")
	}
	for _, name := range sortedNames(l) {
		lim := l[name].clone()
		d, ok := s.exporters[name]
		if !ok {
			return nil, fmt.Errorf("exporter %q is not declared in this data plane's observability ceiling", name)
		}
		if lim.Signals != nil {
			if len(lim.Signals) == 0 {
				return nil, fmt.Errorf("exporter %q: signals must keep at least one signal; use paused to stop the exporter", name)
			}
			seen := map[string]bool{}
			for _, sig := range lim.Signals {
				if !d.cfg.allows(sig) {
					return nil, fmt.Errorf("exporter %q: signal %q is not allowed by the bootstrap ceiling", name, sig)
				}
				if seen[sig] {
					return nil, fmt.Errorf("exporter %q: duplicate signal %q", name, sig)
				}
				seen[sig] = true
			}
			sort.Strings(lim.Signals)
			if len(lim.Signals) == len(d.cfg.Signals) {
				lim.Signals = nil // every declared signal: nothing narrowed
			}
		}
		if lim.MaxContentBytes != 0 {
			if !d.cfg.allows(SignalContent) {
				return nil, fmt.Errorf("exporter %q: max_content_bytes applies only to an exporter that allows content", name)
			}
			if lim.MaxContentBytes < 1 || lim.MaxContentBytes > d.cfg.MaxContentBytes {
				return nil, fmt.Errorf("exporter %q: max_content_bytes must be in [1, %d], the bootstrap cap", name, d.cfg.MaxContentBytes)
			}
			if lim.MaxContentBytes == d.cfg.MaxContentBytes {
				lim.MaxContentBytes = 0
			}
		}
		if lim.MaxSamplePercent != 0 {
			if !d.cfg.allows(SignalTraces) {
				return nil, fmt.Errorf("exporter %q: max_sample_percent applies only to an exporter that allows traces", name)
			}
			if lim.MaxSamplePercent < 1 || lim.MaxSamplePercent > 100 {
				return nil, fmt.Errorf("exporter %q: max_sample_percent must be in [1, 100]", name)
			}
			if lim.MaxSamplePercent == 100 {
				lim.MaxSamplePercent = 0
			}
		}
		if lim.Paused || lim.Signals != nil || lim.MaxContentBytes != 0 || lim.MaxSamplePercent != 0 {
			out[name] = lim
		}
	}
	return out, nil
}

// SetLimits validates and installs l and returns the installed form.
func (s *Set) SetLimits(l Limits) (Limits, error) {
	n, err := s.Normalize(l)
	if err != nil {
		return nil, err
	}
	if s != nil {
		s.limits.Store(&n)
	}
	return n.clone(), nil
}

// Limits returns a copy of the installed runtime limits (empty when none).
func (s *Set) Limits() Limits { return s.current().clone() }

// current is the installed limits map; callers must not modify it.
func (s *Set) current() Limits {
	if s == nil {
		return nil
	}
	if p := s.limits.Load(); p != nil {
		return *p
	}
	return nil
}

// Clamp intersects persisted limits with the current bootstrap ceiling before
// Normalize, choosing the narrower reading whenever the two disagree: an
// exporter no longer declared is dropped (its records cannot be sent anyway),
// a signal no longer declared is removed, a list left with no declared signal
// pauses the exporter, a content cap above the new bootstrap cap is lowered to
// it, and caps for signals the exporter no longer allows are dropped. It never
// widens what the file asked for. Each adjustment is returned as a note.
func (s *Set) Clamp(l Limits) (Limits, []string) {
	out := Limits{}
	var notes []string
	for _, name := range sortedNames(l) {
		lim := l[name].clone()
		var d *declared
		if s != nil {
			d = s.exporters[name]
		}
		if d == nil {
			notes = append(notes, fmt.Sprintf("exporter %q is no longer declared; its limit was dropped", name))
			continue
		}
		if lim.Signals != nil {
			kept := make([]string, 0, len(lim.Signals))
			for _, sig := range lim.Signals {
				if d.cfg.allows(sig) {
					kept = append(kept, sig)
				} else {
					notes = append(notes, fmt.Sprintf("exporter %q no longer declares %s; it stays off", name, sig))
				}
			}
			if len(kept) == 0 {
				kept, lim.Paused = nil, true
				notes = append(notes, fmt.Sprintf("exporter %q kept no declared signal; it is paused", name))
			}
			lim.Signals = kept
		}
		if lim.MaxContentBytes != 0 {
			switch {
			case !d.cfg.allows(SignalContent):
				lim.MaxContentBytes = 0
				notes = append(notes, fmt.Sprintf("exporter %q no longer allows content; max_content_bytes was dropped", name))
			case lim.MaxContentBytes > d.cfg.MaxContentBytes:
				lim.MaxContentBytes = d.cfg.MaxContentBytes
				notes = append(notes, fmt.Sprintf("exporter %q: max_content_bytes was lowered to the bootstrap cap %d", name, d.cfg.MaxContentBytes))
			}
		}
		if lim.MaxSamplePercent != 0 && !d.cfg.allows(SignalTraces) {
			lim.MaxSamplePercent = 0
			notes = append(notes, fmt.Sprintf("exporter %q no longer allows traces; max_sample_percent was dropped", name))
		}
		out[name] = lim
	}
	return out, notes
}

// Exporting reports whether exporter carries signal under l: declared,
// allowed by the bootstrap ceiling and not withheld by l. Pass s.Limits() for
// the live state, or a candidate for a dry run.
func (s *Set) Exporting(l Limits, exporter, signal string) bool {
	if s == nil {
		return false
	}
	return s.allowed(l, exporter, signal)
}

func (s *Set) allowed(l Limits, exporter, signal string) bool {
	d := s.exporters[exporter]
	if d == nil || !d.cfg.allows(signal) {
		return false
	}
	lim := l[exporter]
	if lim.Paused {
		return false
	}
	if lim.Signals == nil {
		return true
	}
	for _, sg := range lim.Signals {
		if sg == signal {
			return true
		}
	}
	return false
}

// SamplePercent lowers a policy trace percent to exporter's
// max_sample_percent under l.
func (s *Set) SamplePercent(l Limits, exporter string, percent int) int {
	if c := l[exporter].MaxSamplePercent; c > 0 && percent > c {
		return c
	}
	return percent
}

// contentLimit is exporter's per-record content cap under l.
func (s *Set) contentLimit(l Limits, exporter string) int64 {
	d := s.exporters[exporter]
	if d == nil {
		return 0
	}
	if c := l[exporter].MaxContentBytes; c > 0 && c < d.cfg.MaxContentBytes {
		return c
	}
	return d.cfg.MaxContentBytes
}

// TraceSelection is one policy trace target: an exporter and its sample
// percent (1..100).
type TraceSelection struct {
	Exporter      string
	SamplePercent int
}

// ceilingAllows reports whether exporter is declared and the bootstrap ceiling
// allows signal. A selection failing it is dropped silently by the decision
// helpers: it creates no per-exporter stats key and is never a limit's doing.
func (s *Set) ceilingAllows(exporter, signal string) bool {
	d := s.exporters[exporter]
	return d != nil && d.cfg.allows(signal)
}

// AuditTargets is the audit-copy half of the governed decision: the policy's
// selection minus exporters the runtime limits withhold. It counts the
// decision, and each exporter a limit withheld, for the console and /metrics.
// A name the ceiling does not allow for audit is dropped without a count.
func (s *Set) AuditTargets(selected []string) []string {
	if s == nil {
		return nil
	}
	lim := s.current()
	var out []string
	withheld := false
	for _, name := range selected {
		switch {
		case !s.ceilingAllows(name, SignalAudit):
		case !s.allowed(lim, name, SignalAudit):
			s.st.count(name, SignalAudit, outcomeWithheld, 1)
			withheld = true
		default:
			out = append(out, name)
		}
	}
	switch {
	case len(out) > 0:
		s.st.decision(SignalAudit, DecisionSelected)
	case withheld:
		s.st.decision(SignalAudit, DecisionWithheld)
	default:
		s.st.decision(SignalAudit, DecisionNoRule)
	}
	return out
}

// ContentTarget is the content half: the policy's single content exporter
// unless the matching rules disagreed (conflict) or a limit withholds it.
func (s *Set) ContentTarget(selected string, conflict bool) string {
	if s == nil {
		return ""
	}
	switch {
	case conflict:
		s.st.decision(SignalContent, DecisionConflict)
		return ""
	case selected == "" || !s.ceilingAllows(selected, SignalContent):
		s.st.decision(SignalContent, DecisionNoRule)
		return ""
	case !s.allowed(s.current(), selected, SignalContent):
		s.st.count(selected, SignalContent, outcomeWithheld, 1)
		s.st.decision(SignalContent, DecisionWithheld)
		return ""
	}
	s.st.decision(SignalContent, DecisionSelected)
	return selected
}

// TraceTargets is the traces half: withheld exporters are dropped and a
// sample percent above an exporter's max_sample_percent is lowered to it. The
// sampling draw itself happens later, in internal/tracing. The legacy `otel`
// baseline is not a selection and never passes through here.
func (s *Set) TraceTargets(selected []TraceSelection) []TraceSelection {
	if s == nil {
		return nil
	}
	lim := s.current()
	var out []TraceSelection
	withheld := false
	for _, t := range selected {
		switch {
		case !s.ceilingAllows(t.Exporter, SignalTraces):
		case !s.allowed(lim, t.Exporter, SignalTraces):
			s.st.count(t.Exporter, SignalTraces, outcomeWithheld, 1)
			withheld = true
		default:
			t.SamplePercent = s.SamplePercent(lim, t.Exporter, t.SamplePercent)
			out = append(out, t)
		}
	}
	switch {
	case len(out) > 0:
		s.st.decision(SignalTraces, DecisionSelected)
	case withheld:
		s.st.decision(SignalTraces, DecisionWithheld)
	default:
		s.st.decision(SignalTraces, DecisionNoRule)
	}
	return out
}

const limitsFileVersion = 1

// limitsFile is the on-disk form of observability.console.state_file.
type limitsFile struct {
	Version   int    `json:"version"`
	Exporters Limits `json:"exporters"`
}

// EncodeLimits returns the canonical file bytes of normalized limits: the
// exact bytes mayu writes to the state file, so their Revision is the limits
// revision the console and the audit record use.
func EncodeLimits(l Limits) []byte {
	if l == nil {
		l = Limits{}
	}
	b, _ := json.MarshalIndent(limitsFile{Version: limitsFileVersion, Exporters: l}, "", "  ") // cannot fail: bools, strings and integers only
	return append(b, '\n')
}

// DecodeLimits parses state file bytes strictly: an unknown field, another
// version or trailing data is an error, because a kill switch that silently
// fails to load would resume export after a restart.
func DecodeLimits(b []byte) (Limits, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f limitsFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode limits: %w", err)
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode limits: trailing data after the JSON object")
	}
	if f.Version != limitsFileVersion {
		return nil, fmt.Errorf("decode limits: unsupported version %d (want %d)", f.Version, limitsFileVersion)
	}
	if f.Exporters == nil {
		f.Exporters = Limits{}
	}
	return f.Exporters, nil
}

// Revision is the console revision of persisted bytes: "sha256:<hex>".
func Revision(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
