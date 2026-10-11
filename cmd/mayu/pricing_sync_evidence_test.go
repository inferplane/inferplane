package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
)

func TestPricingSyncPreservesCacheOverrides(t *testing.T) {
	for _, rc := range []config.RateConfig{
		{InputPerMTok: 1, OutputPerMTok: 2, CacheReadPerMTok: .025},
		{InputPerMTok: 1, OutputPerMTok: 2, CacheWrite5mPerMTok: 1.5},
		{InputPerMTok: 1, OutputPerMTok: 2, CacheWrite1hPerMTok: 3},
		{InputPerMTok: 1, OutputPerMTok: 2, CacheWriteUnsupported: true},
	} {
		cfg := &config.Config{Providers: map[string]config.ProviderConfig{"p": {Type: "bedrock", Region: "ap-northeast-1"}}, Models: map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "apac.zai.glm-5"}}}}, Pricing: config.PricingConfig{Overrides: map[string]map[string]config.RateConfig{"p": {"zai.glm-5": rc}}}}
		api := &fakePricingAPI{pages: map[string][][]string{"ap-northeast-1": {allApne1Docs()}}}
		var out, stderr bytes.Buffer
		if status := runPricingSync(&out, &stderr, cfg, api, ""); status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "cache") {
			t.Fatalf("overwrote cache rates: status=%d out=%s err=%s", status, &out, &stderr)
		}
		if api.calls != 0 {
			t.Fatal("protected override triggered unnecessary catalog fetch")
		}
	}
}

func TestPricingSyncReportsUnsupportedResponsesRoutes(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{"mantle": {Type: "bedrock_responses", BaseURL: "https://bedrock-mantle.us-east-1.api.aws/openai/v1"}, "runtime": {Type: "bedrock", Region: "ap-northeast-1"}}, Models: map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "mantle", Model: "openai.gpt-6-astra"}, {Provider: "runtime", Model: "zai.glm-5"}}}}}
	api := &fakePricingAPI{pages: map[string][][]string{"ap-northeast-1": {allApne1Docs()}}}
	var out, stderr bytes.Buffer
	if status := runPricingSync(&out, &stderr, cfg, api, ""); status != 1 || !strings.Contains(stderr.String(), "UNRESOLVED mantle/openai.gpt-6-astra") || !strings.Contains(stderr.String(), "bedrock_responses") || !strings.Contains(out.String(), "zai.glm-5") {
		t.Fatalf("hidden route: status=%d out=%s err=%s", status, &out, &stderr)
	}
	delete(cfg.Providers, "runtime")
	out.Reset()
	stderr.Reset()
	api.calls = 0
	if status := runPricingSync(&out, &stderr, cfg, api, ""); status != 1 || out.Len() != 0 || api.calls != 0 {
		t.Fatal("unsupported-only config fetched or appeared successful")
	}
}

func TestPriceCandidateRejectsSingleBoundedDimension(t *testing.T) {
	for _, bounds := range []string{
		`"beginRange":"272001","endRange":"Inf",`, `"beginRange":"0","endRange":"272000",`,
		`"beginRange":"0",`, `"endRange":"Inf",`, `"beginRange":"","endRange":"",`,
		`"beginRange":null,"endRange":null,`, `"beginRange":0,"endRange":"Inf",`,
	} {
		doc := strings.Replace(docGLMInput, `"unit":`, bounds+`"unit":`, 1)
		if doc == docGLMInput {
			t.Fatal("fixture edit missed")
		}
		if _, ok := priceCandidateFrom(doc); ok {
			t.Fatal("bounded SKU became a flat price")
		}
	}
	doc := strings.Replace(docGLMInput, `"unit":`, `"beginRange":"0","endRange":"Inf","unit":`, 1)
	if _, ok := priceCandidateFrom(doc); !ok {
		t.Fatal("unbounded SKU rejected")
	}
}
