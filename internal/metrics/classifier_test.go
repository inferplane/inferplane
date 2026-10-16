package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPIIClassifierLabelsAreBounded(t *testing.T) {
	m := New()
	m.ObservePIIClassifier("laya-ko", "r1", "enforce-ish", "maybe", 0.1, 3, 2, 1)
	if got := testutil.ToFloat64(m.classifiers.decisions.WithLabelValues("laya-ko", "r1", "none", "unknown")); got != 1 {
		t.Fatalf("clamped decision count = %v", got)
	}
	if n := testutil.CollectAndCount(m.classifiers.cache); n != 2 {
		t.Fatalf("cache series = %d, want hit and miss", n)
	}
}

// Review Focus 4: a process exports one revision per classifier after a swap.
func TestRetainPIIClassifiersDeletesReplacedSeries(t *testing.T) {
	m := New()
	m.RetainPIIClassifiers(map[string]string{"laya-ko": "r1", "old": "r1"})
	m.ObservePIIClassifier("laya-ko", "r1", "Shadow", "clear", 0.1, 1, 1, 1)
	m.ObservePIIClassifier("old", "r1", "Shadow", "clear", 0.1, 1, 1, 1)
	m.RetainPIIClassifiers(map[string]string{"laya-ko": "r2"})
	if n := testutil.CollectAndCount(m.classifiers.decisions); n != 0 {
		t.Fatalf("%d decision series survived the swap", n)
	}
	if n := testutil.CollectAndCount(m.classifiers.duration); n != 0 {
		t.Fatalf("%d duration series survived the swap", n)
	}
	if n := testutil.CollectAndCount(m.classifiers.windows); n != 1 {
		t.Fatalf("windows series = %d, want the kept classifier only", n)
	}
	if n := testutil.CollectAndCount(m.classifiers.cache); n != 2 {
		t.Fatalf("cache series = %d, want the kept classifier's hit and miss", n)
	}
}
