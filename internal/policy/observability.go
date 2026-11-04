package policy

import (
	"fmt"
	"regexp"
	"sort"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// Observability signals a rule may select. The same strings name the signals
// an exporter allows in mayu's local observability ceiling.
const (
	SignalTraces  = "traces"
	SignalAudit   = "audit"
	SignalContent = "content"
)

// exporterName matches a local ceiling exporter name (DNS-label shaped).
var exporterName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidExporterName reports whether name can identify a ceiling exporter.
func ValidExporterName(name string) bool { return exporterName.MatchString(name) }

// Observability is the validated form of one observability rule. Audit and
// Content are exporter names; "" means the rule says nothing about that signal.
type Observability struct {
	Traces  *TraceExport
	Audit   string
	Content string
}

// TraceExport is one trace target. SamplePercent is 1..100, never 0.
type TraceExport struct {
	Exporter      string
	SamplePercent int
}

// ObservabilityDecision is the merged governed export for one request subject:
// trace targets (sorted by exporter, max percent per exporter), audit exporters
// (sorted, unique) and at most one content exporter. ContentConflict reports
// that matching rules named different content exporters, which is why Content
// is empty; the observability console counts that case separately from "no
// rule".
type ObservabilityDecision struct {
	Traces          []TraceExport
	Audit           []string
	Content         string
	ContentConflict bool
}

func observabilityFromV1Alpha1(wr v1alpha1.Rule, reject func(string, string) *UnsupportedError) (*Observability, error) {
	o := wr.Observability
	if wr.FailurePolicy != v1alpha1.FailOpen {
		return nil, reject(wr.Name, "observability requires FailOpen: export is best-effort and the local audit chain is the evidence of record")
	}
	if o.Traces == nil && o.Audit == nil && o.Content == nil {
		return nil, reject(wr.Name, "observability must set at least one of traces, audit, or content")
	}
	out := &Observability{}
	if o.Traces != nil {
		if !ValidExporterName(o.Traces.Exporter) {
			return nil, reject(wr.Name, fmt.Sprintf("observability.traces.exporter %q must be a declared exporter name (lowercase letters, digits, '-')", o.Traces.Exporter))
		}
		pct := 100
		if o.Traces.SamplePercent != nil {
			pct = *o.Traces.SamplePercent
			if pct < 1 || pct > 100 {
				return nil, reject(wr.Name, fmt.Sprintf("observability.traces.samplePercent must be in [1, 100], got %d (omit the traces block to disable)", pct))
			}
		}
		out.Traces = &TraceExport{Exporter: o.Traces.Exporter, SamplePercent: pct}
	}
	for _, sig := range []struct {
		name string
		in   *v1alpha1.SignalExport
		out  *string
	}{{SignalAudit, o.Audit, &out.Audit}, {SignalContent, o.Content, &out.Content}} {
		if sig.in == nil {
			continue
		}
		if !ValidExporterName(sig.in.Exporter) {
			return nil, reject(wr.Name, fmt.Sprintf("observability.%s.exporter %q must be a declared exporter name (lowercase letters, digits, '-')", sig.name, sig.in.Exporter))
		}
		*sig.out = sig.in.Exporter
	}
	return out, nil
}

// checkObservabilityCeiling verifies every exporter/signal pair a rule names
// against the data plane's local ceiling.
func checkObservabilityCeiling(o *Observability, ceiling func(exporter, signal string) error) error {
	pairs := make([][2]string, 0, 3)
	if o.Traces != nil {
		pairs = append(pairs, [2]string{o.Traces.Exporter, SignalTraces})
	}
	if o.Audit != "" {
		pairs = append(pairs, [2]string{o.Audit, SignalAudit})
	}
	if o.Content != "" {
		pairs = append(pairs, [2]string{o.Content, SignalContent})
	}
	for _, p := range pairs {
		if err := ceiling(p[0], p[1]); err != nil {
			return fmt.Errorf("observability.%s exporter %q: %v", p[1], p[0], err)
		}
	}
	return nil
}

// SetObservabilityCeiling installs the apply-time check that every exporter an
// observability rule names is declared locally and allows that signal. A data
// plane must install it before Reload/ApplyWire; nil skips the check (the
// control plane holds no ceiling of its own, the SetRoutedAndPriced posture).
func (s *Store) SetObservabilityCeiling(f func(exporter, signal string) error) {
	s.observabilityCeiling = f
}

// Observability merges every observability rule whose subject matches the
// request. Traces and audit are unions (trace percent is the maximum per
// exporter). Content is exported only when all matching content selections
// name the same exporter: disagreement disables content for the request,
// because the safe answer for request bodies is not to copy them.
func (s *Store) Observability(team, user string) ObservabilityDecision {
	if s == nil {
		return ObservabilityDecision{}
	}
	snap := s.snap.Load()
	if snap == nil || !snap.observability {
		return ObservabilityDecision{}
	}
	return MergeObservability(snap.policies, team, user)
}

// MergeObservability is the Observability merge over an explicit policy set,
// so the console's dry run compares the decision before and after an edit
// with exactly the rules the request path uses.
func MergeObservability(policies []*Policy, team, user string) ObservabilityDecision {
	var d ObservabilityDecision
	traces := map[string]int{}
	audit := map[string]bool{}
	content := ""
	for _, p := range policies {
		if !p.Subject.matches(team, user) {
			continue
		}
		for _, r := range p.Rules {
			o := r.Observability
			if o == nil {
				continue
			}
			if o.Traces != nil && o.Traces.SamplePercent > traces[o.Traces.Exporter] {
				traces[o.Traces.Exporter] = o.Traces.SamplePercent
			}
			if o.Audit != "" {
				audit[o.Audit] = true
			}
			if o.Content != "" {
				if content != "" && content != o.Content {
					d.ContentConflict = true
				}
				content = o.Content
			}
		}
	}
	for name, pct := range traces {
		d.Traces = append(d.Traces, TraceExport{Exporter: name, SamplePercent: pct})
	}
	sort.Slice(d.Traces, func(i, j int) bool { return d.Traces[i].Exporter < d.Traces[j].Exporter })
	for name := range audit {
		d.Audit = append(d.Audit, name)
	}
	sort.Strings(d.Audit)
	if !d.ContentConflict {
		d.Content = content
	}
	return d
}

func hasObservability(policies []*Policy) bool {
	for _, p := range policies {
		for _, r := range p.Rules {
			if r.Observability != nil {
				return true
			}
		}
	}
	return false
}
