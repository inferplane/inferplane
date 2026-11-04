package otelexport

import (
	"net/http"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/metrics"
)

func decisionCounter(m *metrics.Metrics, signal, decision string) float64 {
	families, err := m.Registry().Gather()
	if err != nil {
		panic(err)
	}
	for _, f := range families {
		if f.GetName() != "inferplane_otel_export_decisions_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range metric.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["signal"] == signal && labels["decision"] == decision {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestStatsRecordFailureWithFixedText(t *testing.T) {
	fastRetry(t)
	col := newCollector(t, http.StatusBadRequest)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}})
	rec, canon := auditRecord()
	if err := s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink).WriteRecord(rec, canon); err != nil {
		t.Fatal(err)
	}
	shutdown(t, s)
	got := s.SignalStats("corp", "audit")
	if got.Outcomes["dropped_export_error"] != 1 || got.Outcomes["sent"] != 0 || got.LastError != "collector status 400" ||
		got.LastErrorAt == "" || got.LastSuccessAt != "" {
		t.Fatalf("stats = %+v", got)
	}
	if len(got.Outcomes) != 6 {
		t.Fatalf("every outcome must be reported, zeros included: %v", got.Outcomes)
	}
	if counter(m, "corp", "audit", "dropped_export_error") != 1 {
		t.Fatal("the console count and the Prometheus series disagree")
	}
}

func TestStatsRecordLastSuccess(t *testing.T) {
	col := newCollector(t)
	s := newTestSet(t, metrics.New(), Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}})
	rec, canon := auditRecord()
	if err := s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink).WriteRecord(rec, canon); err != nil {
		t.Fatal(err)
	}
	shutdown(t, s)
	if got := s.SignalStats("corp", "audit"); got.Outcomes["sent"] != 1 || got.LastSuccessAt == "" || got.LastError != "" {
		t.Fatalf("stats = %+v", got)
	}
}

func TestObserveTraceKeepsAFixedOutcomeSet(t *testing.T) {
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"traces"}})
	s.ObserveTrace("otel", "sent", 3)
	s.ObserveTrace("otel", "sampled_out", 2)
	s.ObserveTrace("corp", "dropped_export_error", 1)
	s.ObserveTrace("corp", "bogus", 7)
	if got := s.SignalStats("otel", "traces"); got.Outcomes["sent"] != 3 || got.Outcomes["sampled_out"] != 2 || got.LastSuccessAt == "" {
		t.Fatalf("legacy stats = %+v", got)
	}
	if got := s.SignalStats("corp", "traces"); got.Outcomes["dropped_export_error"] != 1 || got.LastError != "span export failed" {
		t.Fatalf("corp stats = %+v", got)
	}
	if counter(m, "corp", "traces", "bogus") != 0 {
		t.Fatal("an unknown outcome created a series")
	}
	var nilSet *Set
	nilSet.ObserveTrace("otel", "sent", 1)
	if got := nilSet.SignalStats("otel", "traces"); len(got.Outcomes) != 0 {
		t.Fatalf("nil set stats = %+v", got)
	}
}

func TestDecisionCountsMirrorPrometheus(t *testing.T) {
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"traces"}})
	s.st.decision(SignalAudit, DecisionNoRule)
	s.st.decision(SignalAudit, DecisionNoRule)
	s.st.decision(SignalContent, DecisionConflict)
	rows := map[string]DecisionStats{}
	for _, d := range s.st.decisions() {
		rows[d.Signal] = d
	}
	if len(rows) != 3 || rows["audit"].Decisions["no_rule"] != 2 || rows["content"].Decisions["content_conflict"] != 1 ||
		rows["traces"].Decisions["selected"] != 0 || len(rows["traces"].Decisions) != 4 {
		t.Fatalf("decisions = %+v", rows)
	}
	if decisionCounter(m, "audit", "no_rule") != 2 {
		t.Fatal("the decision series does not match the console count")
	}
}
