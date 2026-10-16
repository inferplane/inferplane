package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// classifierMetrics are the PII classifier collectors. Labels come only from
// validated config (classifier name, revision) and bounded enums.
type classifierMetrics struct {
	decisions *prometheus.CounterVec   // inferplane_pii_classifier_decisions_total
	duration  *prometheus.HistogramVec // inferplane_pii_classifier_duration_seconds
	windows   *prometheus.HistogramVec // inferplane_pii_classifier_windows
	cache     *prometheus.CounterVec   // inferplane_pii_classifier_cache_windows_total
	mu        sync.Mutex
	current   map[string]string // name → revision of the live generation
}

func newClassifierMetrics(reg *prometheus.Registry) *classifierMetrics {
	c := &classifierMetrics{
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inferplane_pii_classifier_decisions_total", Help: "PII classifier results by bounded mode and result.",
		}, []string{"classifier", "revision", "mode", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "inferplane_pii_classifier_duration_seconds", Help: "PII classifier time per request, including cache lookups.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"classifier", "revision", "result"}),
		windows: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "inferplane_pii_classifier_windows", Help: "Windows sent to a PII classifier per request; the CPU-versus-GPU sizing signal.",
			Buckets: []float64{1, 2, 4, 8, 16, 32, 64},
		}, []string{"classifier"}),
		cache: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inferplane_pii_classifier_cache_windows_total", Help: "Verdict-cache lookups of classifier windows by hit or miss.",
		}, []string{"classifier", "result"}),
		current: map[string]string{},
	}
	reg.MustRegister(c.decisions, c.duration, c.windows, c.cache)
	return c
}

// ObservePIIClassifier records one entry's result. mode and result are
// clamped to their enums; name and revision are validated config values.
func (m *Metrics) ObservePIIClassifier(name, revision, mode, result string, seconds float64, windows, hits, misses int) {
	if m == nil || m.classifiers == nil {
		return
	}
	switch mode {
	case "Shadow", "Enforce":
	default:
		mode = "none"
	}
	switch result {
	case "flagged", "clear", "abstain", "error", "skipped":
	default:
		result = "unknown"
	}
	c := m.classifiers
	c.decisions.WithLabelValues(name, revision, mode, result).Inc()
	c.duration.WithLabelValues(name, revision, result).Observe(seconds)
	if windows > 0 {
		c.windows.WithLabelValues(name).Observe(float64(windows))
	}
	if hits > 0 {
		c.cache.WithLabelValues(name, "hit").Add(float64(hits))
	}
	if misses > 0 {
		c.cache.WithLabelValues(name, "miss").Add(float64(misses))
	}
}

// RetainPIIClassifiers runs after each generation swap with the new
// generation's name → revision map. It deletes the series of removed
// classifiers and of replaced revisions, so a process exports one revision
// per classifier. A request still finishing on the previous generation can
// re-create an old series after the swap; that series is no longer tracked,
// so it lives until the process restarts (bounded by the revisions seen).
func (m *Metrics) RetainPIIClassifiers(current map[string]string) {
	if m == nil || m.classifiers == nil {
		return
	}
	c := m.classifiers
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, rev := range c.current {
		next, kept := current[name]
		if kept && next == rev {
			continue
		}
		c.decisions.DeletePartialMatch(prometheus.Labels{"classifier": name, "revision": rev})
		c.duration.DeletePartialMatch(prometheus.Labels{"classifier": name, "revision": rev})
		if !kept {
			c.windows.DeletePartialMatch(prometheus.Labels{"classifier": name})
			c.cache.DeletePartialMatch(prometheus.Labels{"classifier": name})
		}
	}
	c.current = make(map[string]string, len(current))
	for name, rev := range current {
		c.current[name] = rev
	}
}
