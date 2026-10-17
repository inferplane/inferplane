//go:build liveclassifier

package main

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
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

// Ruling 21: laya-serve cuts a state to the checkpoint's token budget (about
// 980 state tokens) without an error and reports the cut in usage. With
// window_runes 4000 the padded request below is one window of about 2,400
// Korean runes (about 1,400 tokens), so the personal data at its end is never
// read: the classifier abstains and the rule's onUninspectable (Block here)
// applies. The request is never Clear and never reaches the external
// provider. With the data first, the part that was read flags, and a flag
// stands even though the window was cut.
func TestLiveClassifierTruncatedWindowIsNeverClear(t *testing.T) {
	url := os.Getenv("INFERPLANE_LIVE_SYSTEMONE_URL")
	if url == "" {
		t.Skip("INFERPLANE_LIVE_SYSTEMONE_URL not set")
	}
	g, key := classifierGatewayWith(t, url, "Enforce", map[string]any{"window_runes": 4000}, 20000)
	const pii = "환자 김민수(서울시 마포구 월드컵로 12 거주)는 지난달 우울증 진단을 받았습니다."
	pad := strings.Repeat("이 문단은 회의록 서식을 맞추기 위한 채움 문장이며 특별한 내용은 없습니다. ", 55)
	if n := utf8.RuneCountInString(pad + pii); n < 2300 || n > 2600 || strings.Contains(pad, "\n") {
		t.Fatalf("fixture: %d runes", n)
	}
	rec := routingCall(t, g, key, pad+pii)
	if rec.Code != 403 || routingProvider(t, g, "public").calls.Load() != 0 || routingProvider(t, g, "private").calls.Load() != 0 {
		t.Fatalf("PII past the token budget: %d %q, public=%d private=%d", rec.Code, rec.Header().Get("x-inferplane-routing-reason"),
			routingProvider(t, g, "public").calls.Load(), routingProvider(t, g, "private").calls.Load())
	}
	if classifierDecisions(t, g, "Enforce", "abstain") != 1 || classifierDecisions(t, g, "Enforce", "clear") != 0 {
		t.Fatalf("decision metrics: abstain=%v clear=%v error=%v", classifierDecisions(t, g, "Enforce", "abstain"),
			classifierDecisions(t, g, "Enforce", "clear"), classifierDecisions(t, g, "Enforce", "error"))
	}
	rec = routingCall(t, g, key, pii+pad)
	if rec.Code != 200 || rec.Header().Get("x-inferplane-routing-reason") != "internal_only" || routingProvider(t, g, "public").calls.Load() != 0 {
		t.Fatalf("PII inside the token budget: %d %q", rec.Code, rec.Header().Get("x-inferplane-routing-reason"))
	}
	if classifierDecisions(t, g, "Enforce", "flagged") != 1 {
		t.Fatalf("a cut window that flags must stay flagged: flagged=%v", classifierDecisions(t, g, "Enforce", "flagged"))
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
