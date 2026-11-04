package router

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/metrics"
)

func TestObserveModelFallbackLogsOncePerPairAndCounts(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	r := New(&live.Holder{})
	m := metrics.New()
	r.SetMetrics(m)
	for range 3 {
		r.ObserveModelFallback("claude-sonnet-9\nforged line", "claude-sonnet-5-5", "family")
	}
	r.ObserveModelFallback(strings.Repeat("y", 500), "claude-sonnet-5-5", "family")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `requested="claude-sonnet-9\nforged line" served=claude-sonnet-5-5`) ||
		strings.Contains(lines[1], strings.Repeat("y", maxFallbackLogName+1)) {
		t.Fatalf("fallback log = %q, want one quoted, bounded line per pair", buf.String())
	}
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range families {
		if f.GetName() != "inferplane_model_fallback_total" {
			continue
		}
		found = true
		if len(f.GetMetric()) != 1 || f.GetMetric()[0].GetCounter().GetValue() != 4 {
			t.Fatalf("fallback counter = %v", f.GetMetric())
		}
		for _, l := range f.GetMetric()[0].GetLabel() {
			if l.GetName() != "source" && l.GetName() != "model" {
				t.Fatalf("unbounded label %s", l.GetName())
			}
		}
	}
	if !found {
		t.Fatal("inferplane_model_fallback_total missing")
	}
}

func TestFallbackLogIsBounded(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	var l fallbackLog
	for i := range maxFallbackLogPairs + 50 {
		l.note(strings.Repeat("m", i%100)+string(rune('a'+i%26))+strings.Repeat("z", i/26), "served", "family")
	}
	if n := strings.Count(buf.String(), "\n"); n != maxFallbackLogPairs+1 {
		t.Fatalf("logged %d lines, want %d pairs plus one cap notice", n, maxFallbackLogPairs+1)
	}
}
