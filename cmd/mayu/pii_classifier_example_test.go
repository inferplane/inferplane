package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/policy"
)

func TestPIIClassifierKoExampleLoads(t *testing.T) {
	t.Setenv("INFERPLANE_ADMIN_TOKEN", "example-placeholder")
	t.Setenv("PII_KO_PUBLIC_KEY", "example-placeholder")
	cfg, err := config.LoadRaw("../../examples/config.pii-classifier-ko.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.ResolveProviders(cfg); err != nil {
		t.Fatal(err)
	}
	st, _, err := live.BuildState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	entries := st.Classifiers().For("pilot-ko")
	if len(entries) != 1 || entries[0].Name != "laya-ko" || entries[0].Mode != classifier.Shadow || entries[0].Revision != "laya-ko-pii-v3s1" || len(st.Classifiers().For("other")) != 0 {
		t.Fatalf("sample classifier scope: %+v", entries)
	}
	h := &live.Holder{}
	h.Swap(st)
	store, err := policy.NewStore(filepath.Join("../..", cfg.Policies[0]))
	if err != nil {
		t.Fatal(err)
	}
	store.SetRoutedAndPriced(h.RoutedAndPriced)
	if err := store.Reload(); err != nil {
		t.Fatalf("sample policy targets: %v", err)
	}
	policies, err := store.MatchingRoutingPolicies("pilot-ko", "")
	if err != nil || len(policies) != 1 {
		t.Fatalf("sample policies: %v %v", policies, err)
	}
	sd := policies[0].Rules[0].SensitiveData
	if sd == nil || sd.OnDetected != v1alpha1.InternalOnly || sd.OnUninspectable != v1alpha1.InternalOnly || !slices.Equal(sd.InternalModels, []string{"private-coder"}) {
		t.Fatalf("sample privacy rule: %+v", sd)
	}
	if st.DataBoundary("approved-private") != "internal" || st.DataBoundary("approved-public") != "external" {
		t.Fatal("sample boundaries lost")
	}
}

// fakeSystemone flags any window that contains marker. Like laya-serve, it
// reports in usage that it read every window whole.
func fakeSystemone(t *testing.T, marker string) *httptest.Server {
	t.Helper()
	return fakeSystemoneCounting(t, marker, nil)
}

func fakeSystemoneCounting(t *testing.T, marker string, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		var body struct {
			States []string `json:"states"`
		}
		if r.URL.Path != "/v1/systemone/batch" || json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		results := make([]map[string]any, len(body.States))
		for i, s := range body.States {
			p := 0.01
			if strings.Contains(s, marker) {
				p = 0.99
			}
			results[i] = map[string]any{"answers": map[string]any{"pii": map[string]any{"type": "noul", "noul": p}},
				"usage": map[string]any{"input_tokens": 64, "state_tokens": 32, "state_tokens_dropped": 0, "truncated": false}}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func classifierGateway(t *testing.T, endpoint, mode string) (*gateway, string) {
	t.Helper()
	doc := routingPrivacyDoc() // team engineering: InternalOnly to "private", Block when uninspectable
	path := routingAcceptanceConfig(t, &doc, func(cfg map[string]any, _ string) {
		cfg["pii_classification"] = map[string]any{"classifiers": []map[string]any{{
			"name": "laya-ko", "type": "systemone", "mode": mode, "revision": "test",
			"endpoint": endpoint, "data_boundary": "internal", "options": map[string]any{"model": "multilingual"},
		}}}
	})
	return bootRoutingAcceptance(t, path)
}

func classifierDecisions(t *testing.T, g *gateway, mode, result string) float64 {
	t.Helper()
	families, err := g.metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != "inferplane_pii_classifier_decisions_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["classifier"] == "laya-ko" && labels["mode"] == mode && labels["result"] == result {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestGatewayClassifierRestrictsDestinationEndToEnd(t *testing.T) {
	const address = "고객 배송지는 서울시 마포구 월드컵로 12입니다"
	t.Run("Enforce", func(t *testing.T) {
		g, key := classifierGateway(t, fakeSystemone(t, "서울시 마포구").URL, "Enforce")
		rec := routingCall(t, g, key, address)
		if rec.Code != 200 || rec.Header().Get("x-inferplane-routing-reason") != "internal_only" {
			body, _ := io.ReadAll(rec.Body)
			t.Fatalf("%d %q %s", rec.Code, rec.Header().Get("x-inferplane-routing-reason"), body)
		}
		if routingProvider(t, g, "private").calls.Load() != 1 || routingProvider(t, g, "public").calls.Load() != 0 {
			t.Fatal("a flagged request reached the external provider")
		}
		if classifierDecisions(t, g, "Enforce", "flagged") != 1 {
			t.Fatal("decision metric missing")
		}
	})
	t.Run("Shadow", func(t *testing.T) {
		g, key := classifierGateway(t, fakeSystemone(t, "서울시 마포구").URL, "Shadow")
		if rec := routingCall(t, g, key, address); rec.Code != 200 {
			t.Fatalf("%d", rec.Code)
		}
		if routingProvider(t, g, "public").calls.Load() != 1 || classifierDecisions(t, g, "Shadow", "flagged") != 1 {
			t.Fatal("Shadow changed routing or recorded nothing")
		}
	})
	t.Run("Enforce with an unreachable classifier fails closed", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		g, key := classifierGateway(t, dead.URL, "Enforce")
		if rec := routingCall(t, g, key, "평범한 코딩 질문"); rec.Code != 403 {
			t.Fatalf("status %d, want 403 (onUninspectable Block)", rec.Code)
		}
		if routingProvider(t, g, "public").calls.Load() != 0 || classifierDecisions(t, g, "Enforce", "error") != 1 {
			t.Fatal("an unreachable classifier did not fail closed")
		}
	})
}
