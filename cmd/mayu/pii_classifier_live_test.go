//go:build liveclassifier

package main

import (
	"os"
	"testing"
)

// Run: INFERPLANE_LIVE_SYSTEMONE_URL=http://127.0.0.1:18095 go test -tags liveclassifier ./cmd/mayu/ -run TestLive -v
func TestLiveClassifierEnforceAndCache(t *testing.T) {
	url := os.Getenv("INFERPLANE_LIVE_SYSTEMONE_URL")
	if url == "" {
		t.Skip("INFERPLANE_LIVE_SYSTEMONE_URL not set")
	}
	g, key := classifierGateway(t, url, "Enforce")
	if rec := routingCall(t, g, key, "고객 김민수(010-2345-6789)의 배송지는 서울시 마포구 월드컵로 12입니다."); rec.Code != 200 || rec.Header().Get("x-inferplane-routing-reason") != "internal_only" {
		t.Fatalf("PII request: %d %q", rec.Code, rec.Header().Get("x-inferplane-routing-reason"))
	}
	const clean = "이 Go 함수의 에러 처리를 errors.Join으로 바꿔줘."
	for i := 0; i < 2; i++ {
		if rec := routingCall(t, g, key, clean); rec.Code != 200 || rec.Header().Get("x-inferplane-routing-reason") == "internal_only" {
			t.Fatalf("clean request %d: %d %q", i+1, rec.Code, rec.Header().Get("x-inferplane-routing-reason"))
		}
	}
	if routingProvider(t, g, "private").calls.Load() != 1 || routingProvider(t, g, "public").calls.Load() != 2 {
		t.Fatal("destinations do not match the verdicts")
	}
	// Decisions are counted once per request. The flagged request is one
	// flagged decision. Both clean requests end Clear: the first because the
	// classifier scored every window Clear, the second because every window was
	// a cache hit (runEntry sets OutcomeClear when nothing is pending), so
	// clear == 2.
	if classifierDecisions(t, g, "Enforce", "flagged") != 1 || classifierDecisions(t, g, "Enforce", "clear") != 2 {
		t.Fatalf("decision metrics: flagged=%v clear=%v", classifierDecisions(t, g, "Enforce", "flagged"), classifierDecisions(t, g, "Enforce", "clear"))
	}
	// The verdict cache stores only Clear verdicts of whole windows, so the
	// flagged first request caches nothing. The second clean request repeats
	// the first clean request's only window, which was cached Clear.
	if cacheHits(t, g) < 1 {
		t.Fatal("the second clean request read nothing from the verdict cache")
	}
}

func cacheHits(t *testing.T, g *gateway) float64 {
	t.Helper()
	families, err := g.metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != "inferplane_pii_classifier_cache_windows_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == "hit" {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
