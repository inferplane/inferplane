package otelexport

import (
	"sort"
	"time"
)

// ExporterStatus is the console's view of one declared exporter. Header values
// never appear, only header names.
type ExporterStatus struct {
	Name                     string        `json:"name"`
	Endpoint                 string        `json:"endpoint"`
	Protocol                 string        `json:"protocol"`
	TLS                      bool          `json:"tls"`
	HeaderNames              []string      `json:"header_names"`
	DataBoundary             string        `json:"data_boundary,omitempty"`
	Signals                  []string      `json:"signals"`
	EffectiveSignals         []string      `json:"effective_signals"`
	MaxContentBytes          int64         `json:"max_content_bytes,omitempty"`
	EffectiveMaxContentBytes int64         `json:"effective_max_content_bytes,omitempty"`
	QueueLimitBytes          int64         `json:"queue_limit_bytes,omitempty"`
	QueuedBytes              int64         `json:"queued_bytes"`
	Limit                    Limit         `json:"limit"`
	Stats                    []SignalStats `json:"stats"`
}

// Status is the console snapshot of the declared exporters and the governed
// decisions. Counters are per process since CountersSince.
type Status struct {
	ServiceName   string           `json:"service_name"`
	CountersSince string           `json:"counters_since"`
	Exporters     []ExporterStatus `json:"exporters"`
	Decisions     []DecisionStats  `json:"decisions"`
}

// Status returns the console snapshot. A nil Set has no exporters.
func (s *Set) Status() Status {
	if s == nil {
		return Status{Exporters: []ExporterStatus{}, Decisions: []DecisionStats{}}
	}
	lim := s.current()
	names := make([]string, 0, len(s.exporters))
	for name := range s.exporters {
		names = append(names, name)
	}
	sort.Strings(names)
	out := Status{ServiceName: s.serviceName, CountersSince: s.st.since.UTC().Format(time.RFC3339),
		Exporters: make([]ExporterStatus, 0, len(names)), Decisions: s.st.decisions()}
	for _, name := range names {
		d := s.exporters[name]
		e := d.cfg
		signals := append([]string(nil), e.Signals...)
		sort.Strings(signals)
		headers := make([]string, 0, len(e.Headers))
		for h := range e.Headers {
			headers = append(headers, h)
		}
		sort.Strings(headers)
		row := ExporterStatus{Name: name, Endpoint: e.Endpoint, Protocol: protocolName(e.Protocol), TLS: !e.Insecure,
			HeaderNames: headers, DataBoundary: e.DataBoundary, Signals: signals, EffectiveSignals: []string{},
			Limit: lim[name].clone(), Stats: make([]SignalStats, 0, len(signals))}
		for _, sig := range signals {
			if s.allowed(lim, name, sig) {
				row.EffectiveSignals = append(row.EffectiveSignals, sig)
			}
			row.Stats = append(row.Stats, s.st.signalStats(name, sig))
		}
		if e.allows(SignalContent) {
			row.MaxContentBytes = e.MaxContentBytes
			row.EffectiveMaxContentBytes = s.contentLimit(lim, name)
		}
		if d.logs != nil {
			row.QueueLimitBytes = d.logs.limit
			row.QueuedBytes = d.logs.queued.Load()
		}
		out.Exporters = append(out.Exporters, row)
	}
	return out
}

func protocolName(p string) string {
	if p == "" {
		return "http"
	}
	return p
}
