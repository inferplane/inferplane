package otelexport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/metrics"
)

func TestStatusShowsDeclaredExportersWithoutHeaderValues(t *testing.T) {
	s := newTestSet(t, metrics.New(),
		Exporter{Name: "corp", Endpoint: "collector.internal:4318", Headers: map[string]string{"authorization": "Bearer status-secret"}, Signals: []string{"traces", "audit"}},
		Exporter{Name: "vault", Endpoint: "127.0.0.1:1", Protocol: "grpc", Insecure: true, Signals: []string{"content"}, DataBoundary: "internal", MaxContentBytes: 1 << 20, QueueBytes: 4 << 20})
	t.Cleanup(func() { shutdown(t, s) })
	if _, err := s.SetLimits(Limits{"corp": {Signals: []string{"audit"}}, "vault": {MaxContentBytes: 4096}}); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "status-secret") {
		t.Fatalf("status leaked a header value: %s", b)
	}
	if st.ServiceName != "mayu-test" || st.CountersSince == "" || len(st.Exporters) != 2 || len(st.Decisions) != 3 {
		t.Fatalf("status = %+v", st)
	}
	corp, vault := st.Exporters[0], st.Exporters[1]
	if corp.Name != "corp" || corp.Protocol != "http" || !corp.TLS || strings.Join(corp.HeaderNames, ",") != "authorization" ||
		strings.Join(corp.Signals, ",") != "audit,traces" || strings.Join(corp.EffectiveSignals, ",") != "audit" ||
		corp.QueueLimitBytes != defaultQueueBytes || corp.MaxContentBytes != 0 || len(corp.Stats) != 2 {
		t.Fatalf("corp = %+v", corp)
	}
	if vault.Protocol != "grpc" || vault.TLS || vault.DataBoundary != "internal" || vault.MaxContentBytes != 1<<20 ||
		vault.EffectiveMaxContentBytes != 4096 || vault.QueueLimitBytes != 4<<20 || vault.Limit.MaxContentBytes != 4096 {
		t.Fatalf("vault = %+v", vault)
	}
	var nilSet *Set
	if ns := nilSet.Status(); ns.Exporters == nil || len(ns.Exporters) != 0 || ns.CountersSince != "" {
		t.Fatalf("nil set status = %+v", ns)
	}
}
