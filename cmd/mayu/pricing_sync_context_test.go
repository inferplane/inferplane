package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
)

func TestPricingSyncDoesNotFlattenDeclaredContextTiers(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"p": {Type: "bedrock", Region: "ap-northeast-1"}},
		Models:    map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "zai.glm-5"}}}},
		Pricing:   config.PricingConfig{Overrides: map[string]map[string]config.RateConfig{"p": {"zai.glm-5": {InputPerMTok: 1, OutputPerMTok: 2, ShortContextLimit: 100, LongContext: &config.RateConfig{InputPerMTok: 2, OutputPerMTok: 3}}}}},
	}
	api := &fakePricingAPI{pages: map[string][][]string{"ap-northeast-1": {allApne1Docs()}}}
	var out, stderr bytes.Buffer
	if status := runPricingSync(&out, &stderr, cfg, api, ""); status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "context") {
		t.Fatalf("flattened tiered row: status=%d out=%s err=%s", status, out.String(), stderr.String())
	}
}
