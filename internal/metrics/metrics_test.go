package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestTokenUsageCounter(t *testing.T) {
	m := New()
	m.ObserveTokenUsage("input", "claude-sonnet-4-6", "anthropic-direct", "platform-eng", 1200)
	m.ObserveTokenUsage("output", "claude-sonnet-4-6", "anthropic-direct", "platform-eng", 850)
	got := testutil.ToFloat64(m.tokenUsage.WithLabelValues("input", "claude-sonnet-4-6", "anthropic-direct", "platform-eng"))
	if got != 1200 {
		t.Fatalf("input token usage = %v, want 1200", got)
	}
}

func TestRequestsTotalAndExposition(t *testing.T) {
	m := New()
	m.ObserveRequest("anthropic", "claude-sonnet-4-6", "anthropic-direct", "platform-eng", 200, 1.5, 0.4)
	// gather and confirm the metric names are present with GenAI naming
	out := gather(t, m)
	for _, want := range []string{
		"gen_ai_client_token_usage_total",
		"gen_ai_server_request_duration_seconds",
		"gen_ai_server_time_to_first_token_seconds",
		"inferplane_requests_total",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metric %q not exposed in:\n%s", want, out)
		}
	}
}

func TestCircuitStateGauge(t *testing.T) {
	m := New()
	m.SetCircuitState("anthropic-direct", 2) // open
	got := testutil.ToFloat64(m.circuitState.WithLabelValues("anthropic-direct"))
	if got != 2 {
		t.Fatalf("circuit state = %v, want 2", got)
	}
}

// ADR-041: the substitution counter is labeled team/from_model/to_model —
// all config/policy-declared values, never raw client input (mirrors
// ObserveFallback's cardinality posture).
func TestModelSubstitutionCounter(t *testing.T) {
	m := New()
	m.ObserveModelSubstitution("ml-platform", "claude-haiku-4-5", "glm-4.7-gpu")
	got := testutil.ToFloat64(m.substitution.WithLabelValues("ml-platform", "claude-haiku-4-5", "glm-4.7-gpu"))
	if got != 1 {
		t.Fatalf("substitution count = %v, want 1", got)
	}
}

func TestNilMetricsNoPanic(t *testing.T) {
	var m *Metrics // nil
	m.ObserveRequest("a", "b", "c", "d", 200, 1, 0)
	m.ObserveTokenUsage("input", "m", "p", "t", 10)
	m.ObserveFallback("m", "from", "to", "reason")
	m.ObserveModelSubstitution("t", "from", "to")
	m.SetCircuitState("p", 2)
	m.SetQuotaUtilization("t", "day", 0.5)
	m.AddBudgetSpend("t", "m", "total", 1.5)
	m.IncPricingMiss("p", "m")
	m.IncAuditFailure("file")
	m.SetAuditBufferUtilization(0.1)
	// no panic = pass
}

func gather(t *testing.T, m *Metrics) string {
	t.Helper()
	mfs, err := m.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, mf := range mfs {
		sb.WriteString(mf.GetName())
		sb.WriteString("\n")
	}
	_ = prometheus.NewRegistry
	return sb.String()
}

func TestIncUsageWindowDropped(t *testing.T) {
	m := New()
	m.IncUsageWindowDropped()
	m.IncUsageWindowDropped()
	if v := testutil.ToFloat64(m.usageDropped); v != 2 {
		t.Fatalf("inferplane_usage_windows_dropped_total = %v, want 2", v)
	}
	var nilM *Metrics
	nilM.IncUsageWindowDropped() // nil-safe like every other hook
}

func TestSetBudgetStoreRejections(t *testing.T) {
	m := New()
	m.SetBudgetStoreRejections(3)
	if v := testutil.ToFloat64(m.budgetRejected); v != 3 {
		t.Fatalf("inferplane_budget_store_rejected_total = %v, want 3", v)
	}
	m.SetBudgetStoreRejections(7) // a Set, not an Inc — the caller reports the store's own cumulative total
	if v := testutil.ToFloat64(m.budgetRejected); v != 7 {
		t.Fatalf("inferplane_budget_store_rejected_total = %v, want 7", v)
	}
	var nilM *Metrics
	nilM.SetBudgetStoreRejections(1) // nil-safe like every other hook
}
