package otelexport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/metrics"
)

func limitSet(t *testing.T, m *metrics.Metrics) *Set {
	t.Helper()
	s := newTestSet(t, m,
		Exporter{Name: "corp", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"traces", "audit"}},
		Exporter{Name: "vault", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"content"}, DataBoundary: "internal", MaxContentBytes: 1 << 20, QueueBytes: 4 << 20})
	t.Cleanup(func() { shutdown(t, s) })
	return s
}

func TestNormalizeOnlyNarrows(t *testing.T) {
	s := limitSet(t, metrics.New())
	for _, tc := range []struct {
		name string
		l    Limits
		want string
	}{
		{"undeclared exporter", Limits{"other": {Paused: true}}, "not declared"},
		{"undeclared signal", Limits{"corp": {Signals: []string{"content"}}}, "not allowed"},
		{"empty signals", Limits{"corp": {Signals: []string{}}}, "use paused"},
		{"duplicate signal", Limits{"corp": {Signals: []string{"audit", "audit"}}}, "duplicate"},
		{"content cap raised", Limits{"vault": {MaxContentBytes: 2 << 20}}, "max_content_bytes"},
		{"negative content cap", Limits{"vault": {MaxContentBytes: -1}}, "max_content_bytes"},
		{"content cap without content", Limits{"corp": {MaxContentBytes: 10}}, "max_content_bytes"},
		{"sample percent above 100", Limits{"corp": {MaxSamplePercent: 101}}, "max_sample_percent"},
		{"sample percent without traces", Limits{"vault": {MaxSamplePercent: 50}}, "max_sample_percent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Normalize(tc.l); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Normalize(%v) = %v, want an error containing %q", tc.l, err, tc.want)
			}
		})
	}
	var nilSet *Set
	if _, err := nilSet.Normalize(Limits{"corp": {Paused: true}}); err == nil {
		t.Fatal("a data plane without exporters accepted a limit")
	}
}

func TestNormalizeCanonicalizes(t *testing.T) {
	s := limitSet(t, metrics.New())
	got, err := s.Normalize(Limits{
		"corp":  {Signals: []string{"traces", "audit"}, MaxSamplePercent: 100}, // the declared set and 100%: no limit
		"vault": {MaxContentBytes: 512},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["corp"]; ok || got["vault"].MaxContentBytes != 512 || len(got) != 1 {
		t.Fatalf("normalized = %+v", got)
	}
	a, _ := s.Normalize(Limits{"corp": {Signals: []string{"audit"}}, "vault": {MaxContentBytes: 1 << 20}})
	b, _ := s.Normalize(Limits{"corp": {Signals: []string{"audit"}, MaxSamplePercent: 100}})
	if Revision(EncodeLimits(a)) != Revision(EncodeLimits(b)) {
		t.Fatalf("equivalent limits must share a revision:\n%s\n%s", EncodeLimits(a), EncodeLimits(b))
	}
}

func TestLimitsWithholdAtDecisionTime(t *testing.T) {
	m := metrics.New()
	s := limitSet(t, m)
	if got := s.AuditTargets([]string{"corp"}); strings.Join(got, ",") != "corp" {
		t.Fatalf("audit before the limit = %v", got)
	}
	if _, err := s.SetLimits(Limits{"corp": {Paused: true}}); err != nil {
		t.Fatal(err)
	}
	if got := s.AuditTargets([]string{"corp"}); got != nil {
		t.Fatalf("a paused exporter still received audit: %v", got)
	}
	if got := s.TraceTargets([]TraceSelection{{Exporter: "corp", SamplePercent: 50}}); got != nil {
		t.Fatalf("a paused exporter still received traces: %v", got)
	}
	if got := s.AuditTargets(nil); got != nil {
		t.Fatal("no selection must export nothing")
	}
	if s.ContentTarget("vault", false) != "vault" || s.ContentTarget("vault", true) != "" || s.ContentTarget("", false) != "" {
		t.Fatal("content decision wrong")
	}
	rows := map[string]DecisionStats{}
	for _, d := range s.st.decisions() {
		rows[d.Signal] = d
	}
	if a := rows["audit"].Decisions; a["selected"] != 1 || a["withheld_limit"] != 1 || a["no_rule"] != 1 {
		t.Fatalf("audit decisions = %v", a)
	}
	if c := rows["content"].Decisions; c["selected"] != 1 || c["content_conflict"] != 1 || c["no_rule"] != 1 {
		t.Fatalf("content decisions = %v", c)
	}
	if rows["traces"].Decisions["withheld_limit"] != 1 {
		t.Fatalf("traces decisions = %v", rows["traces"].Decisions)
	}
	if got := s.SignalStats("corp", "audit").Outcomes["withheld_limit"]; got != 1 || counter(m, "corp", "audit", "withheld_limit") != 1 {
		t.Fatalf("withheld audit count = %d", got)
	}
	if _, err := s.SetLimits(Limits{}); err != nil {
		t.Fatal(err)
	}
	if got := s.AuditTargets([]string{"corp"}); strings.Join(got, ",") != "corp" {
		t.Fatalf("clearing the limit did not restore audit: %v", got)
	}
}

func TestTraceTargetsCapSamplePercent(t *testing.T) {
	s := limitSet(t, metrics.New())
	if _, err := s.SetLimits(Limits{"corp": {MaxSamplePercent: 10}}); err != nil {
		t.Fatal(err)
	}
	if got := s.TraceTargets([]TraceSelection{{Exporter: "corp", SamplePercent: 50}}); len(got) != 1 || got[0].SamplePercent != 10 {
		t.Fatalf("capped = %+v", got)
	}
	if got := s.TraceTargets([]TraceSelection{{Exporter: "corp", SamplePercent: 5}}); len(got) != 1 || got[0].SamplePercent != 5 {
		t.Fatalf("a lower percent must stay: %+v", got)
	}
	if _, err := s.SetLimits(Limits{"corp": {Signals: []string{"audit"}}}); err != nil {
		t.Fatal(err)
	}
	if got := s.TraceTargets([]TraceSelection{{Exporter: "corp", SamplePercent: 50}}); got != nil {
		t.Fatalf("traces dropped from the signal list still exported: %+v", got)
	}
	if got := s.AuditTargets([]string{"corp"}); len(got) != 1 {
		t.Fatalf("audit kept in the signal list was withheld: %v", got)
	}
}

func TestContentCapIsLoweredAtEmit(t *testing.T) {
	col := newCollector(t)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "vault", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"content"}, MaxContentBytes: 1 << 20})
	if _, err := s.SetLimits(Limits{"vault": {MaxContentBytes: 10}}); err != nil {
		t.Fatal(err)
	}
	s.EmitContent("vault", Content{RecordID: "big", Request: []byte(strings.Repeat("x", 20))})
	s.EmitContent("vault", Content{RecordID: "small", Request: []byte("ok")})
	shutdown(t, s)
	if n := len(col.records()); n != 1 {
		t.Fatalf("exported %d content records, want only the one under the lowered cap", n)
	}
	if counter(m, "vault", "content", "dropped_oversize") != 1 {
		t.Fatal("the oversize drop under the lowered cap was not counted")
	}
}

// A limit installed after the decision still stops the record in emit.
func TestEmitRechecksLimits(t *testing.T) {
	col := newCollector(t)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}})
	sink := s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink)
	if _, err := s.SetLimits(Limits{"corp": {Paused: true}}); err != nil {
		t.Fatal(err)
	}
	rec, canon := auditRecord()
	if err := sink.WriteRecord(rec, canon); err != nil {
		t.Fatal(err)
	}
	shutdown(t, s)
	if n := len(col.records()); n != 0 || counter(m, "corp", "audit", "withheld_limit") != 1 {
		t.Fatalf("records = %d, withheld = %v", n, counter(m, "corp", "audit", "withheld_limit"))
	}
}

func TestLimitsEncodingIsCanonicalAndStrict(t *testing.T) {
	s := limitSet(t, metrics.New())
	l, err := s.Normalize(Limits{"corp": {Paused: true}, "vault": {MaxContentBytes: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	b := EncodeLimits(l)
	back, err := DecodeLimits(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(EncodeLimits(back)) != string(b) || !strings.HasPrefix(Revision(b), "sha256:") {
		t.Fatalf("round trip changed the bytes:\n%s", b)
	}
	if got := string(EncodeLimits(nil)); got != "{\n  \"version\": 1,\n  \"exporters\": {}\n}\n" {
		t.Fatalf("empty limits = %q", got)
	}
	for _, bad := range []string{
		`{"version":1,"exporters":{"corp":{"pased":true}}}`,
		`{"version":2,"exporters":{}}`,
		`{"version":1,"exporters":{}} {}`,
		`{"version":1,"exporters":{}}}`,
		`{"version":1,"exporters":{}}]`,
		`not json`,
	} {
		if _, err := DecodeLimits([]byte(bad)); err == nil {
			t.Fatalf("DecodeLimits accepted %s", bad)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil || decoded["version"] != float64(1) {
		t.Fatalf("encoded limits = %s (%v)", b, err)
	}
}

func TestClampNeverWidens(t *testing.T) {
	s := limitSet(t, metrics.New())
	got, notes := s.Clamp(Limits{
		"gone":  {Paused: true},
		"corp":  {Signals: []string{}, MaxContentBytes: 5},                                                // no signal kept; corp has no content
		"vault": {Signals: []string{"content", "traces"}, MaxContentBytes: 8 << 20, MaxSamplePercent: 20}, // traces undeclared; cap above bootstrap
	})
	if _, ok := got["gone"]; ok {
		t.Fatal("an undeclared exporter's limit survived the clamp")
	}
	if c := got["corp"]; !c.Paused || c.Signals != nil || c.MaxContentBytes != 0 {
		t.Fatalf("corp = %+v: a limit that keeps no declared signal must pause", c)
	}
	if v := got["vault"]; strings.Join(v.Signals, ",") != "content" || v.MaxContentBytes != 1<<20 || v.MaxSamplePercent != 0 || v.Paused {
		t.Fatalf("vault = %+v", v)
	}
	if len(notes) != 6 {
		t.Fatalf("notes = %q", notes)
	}
	if _, err := s.Normalize(got); err != nil {
		t.Fatalf("clamped limits must normalize: %v", err)
	}
}

// An undeclared (or ceiling-disallowed) selection is dropped silently: no
// per-exporter series and no withheld decision unless a limit withheld one.
func TestUndeclaredSelectionIsDroppedSilently(t *testing.T) {
	m := metrics.New()
	s := limitSet(t, m)
	if got := s.AuditTargets([]string{"ghost"}); got != nil {
		t.Fatalf("audit = %v", got)
	}
	if got := s.AuditTargets([]string{"vault"}); got != nil { // vault does not allow audit
		t.Fatalf("audit = %v", got)
	}
	if got := s.ContentTarget("ghost", false); got != "" {
		t.Fatalf("content = %q", got)
	}
	if got := s.TraceTargets([]TraceSelection{{Exporter: "ghost", SamplePercent: 5}, {Exporter: "vault", SamplePercent: 5}}); got != nil {
		t.Fatalf("traces = %v", got)
	}
	for _, d := range s.st.decisions() {
		if d.Decisions["withheld_limit"] != 0 || d.Decisions["no_rule"] == 0 {
			t.Fatalf("%s decisions = %v", d.Signal, d.Decisions)
		}
	}
	for _, name := range []string{"ghost", "vault"} {
		for _, sig := range []string{"audit", "traces", "content"} {
			if counter(m, name, sig, "withheld_limit") != 0 || s.SignalStats(name, sig).Outcomes["withheld_limit"] != 0 {
				t.Fatalf("%s/%s got a withheld count", name, sig)
			}
		}
	}
	// With a limit pausing a declared exporter, a mixed selection still counts
	// only the declared one.
	if _, err := s.SetLimits(Limits{"corp": {Paused: true}}); err != nil {
		t.Fatal(err)
	}
	if got := s.AuditTargets([]string{"ghost", "corp"}); got != nil {
		t.Fatalf("audit = %v", got)
	}
	if counter(m, "ghost", "audit", "withheld_limit") != 0 || counter(m, "corp", "audit", "withheld_limit") != 1 {
		t.Fatal("withheld counts wrong for a mixed selection")
	}
}
