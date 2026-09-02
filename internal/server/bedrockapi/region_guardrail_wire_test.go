package bedrockapi

// D6/D7 wire coverage for the BEDROCK ingress (strategy P0 "Guardrail /
// residency": a configured guardrail and region lock apply on every egress
// path, with no opt-out reachable from routing config). The anthropic and
// openai ingresses have carried these fences since ADR-019/020; this
// ingress — the one Bedrock-mode Claude Code uses — did not, so a
// regression here would have shipped without a failing test.

import (
	"net/http/httptest"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
)

func TestInvokeTeamPolicy_RegionRestrictedTeamBlocksUnlabeledTarget(t *testing.T) {
	cap := &captureProvider{}
	provs := map[string]providers.Provider{"p": cap}
	models := map[string]config.ModelConfig{
		"claude-x": {Targets: []config.Target{{Provider: "p", Model: "anthropic.claude-x-v1:0"}}},
	}
	h := holderFor(provs, models)
	handler := NewInvokeHandler(router.New(h), h, false)
	handler.SetTeamPolicy(func(team string) (keystore.TeamRecord, bool) {
		return keystore.TeamRecord{AllowedRegions: []string{"eu"}}, true
	})

	req := invokeReq("claude-x", `{"anthropic_version":"bedrock-2023-05-31","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "restricted", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 403 {
		t.Fatalf("status %d, want 403 (region_blocked): %s", rec.Code, rec.Body)
	}
	if cap.last != nil {
		t.Fatal("provider must not be called once its only target is region-filtered out")
	}
}

func TestInvokeTeamPolicy_GuardrailOverrideReachesProxyRequest(t *testing.T) {
	cap := &captureProvider{}
	provs := map[string]providers.Provider{"p": cap}
	models := map[string]config.ModelConfig{
		"claude-x": {Targets: []config.Target{{Provider: "p", Model: "anthropic.claude-x-v1:0"}}},
	}
	h := holderFor(provs, models)
	handler := NewInvokeHandler(router.New(h), h, false)
	handler.SetTeamPolicy(func(team string) (keystore.TeamRecord, bool) {
		if team == "acme" {
			return keystore.TeamRecord{GuardrailID: "gr-team", GuardrailVersion: "2"}, true
		}
		return keystore.TeamRecord{}, false
	})

	req := invokeReq("claude-x", `{"anthropic_version":"bedrock-2023-05-31","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "acme", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if cap.last == nil {
		t.Fatal("provider not called")
	}
	if cap.last.GuardrailID != "gr-team" || cap.last.GuardrailVersion != "2" {
		t.Fatalf("guardrail override not stamped on ProxyRequest: %q/%q", cap.last.GuardrailID, cap.last.GuardrailVersion)
	}
}
