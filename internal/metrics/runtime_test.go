package metrics

import (
	"runtime"
	"strings"
	"testing"
)

func TestRegistryExposesRuntimeAndProcessCollectors(t *testing.T) {
	m := New()
	m.ObserveRequest("anthropic", "model-a", "prov-a", "team-a", 200, 0.1, 0)
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]map[string]bool{}
	for _, mf := range mfs {
		names := map[string]bool{}
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				names[lp.GetName()] = true
				if strings.HasPrefix(lp.GetValue(), "ik_") {
					t.Errorf("%s exposes a key-shaped label value", mf.GetName())
				}
			}
		}
		labels[mf.GetName()] = names
	}
	want := []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "go_memstats_heap_inuse_bytes", "go_gc_duration_seconds"}
	if runtime.GOOS == "linux" {
		want = append(want, "process_resident_memory_bytes", "process_open_fds", "process_cpu_seconds_total")
	}
	for _, name := range want {
		if _, ok := labels[name]; !ok {
			t.Errorf("/metrics lacks %s", name)
		}
	}
	for name, set := range labels {
		if !strings.HasPrefix(name, "go_") && !strings.HasPrefix(name, "process_") {
			continue
		}
		for label := range set {
			if label != "version" && label != "quantile" {
				t.Errorf("runtime family %s has unexpected label %q", name, label)
			}
		}
	}
}
