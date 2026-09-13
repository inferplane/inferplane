package router

import (
	"reflect"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

func TestDescribeModelsKeepsConfiguredTargetsDespiteBreakers(t *testing.T) {
	r := newTestRouterWithFallbacks(map[string]providers.Provider{
		"first": mockprovider.New("unused"), "second": mockprovider.New("unused"), "fallback": mockprovider.New("unused"),
	}, map[string]config.ModelConfig{
		"a": {Targets: []config.Target{{Provider: "first", Model: "a1"}, {Provider: "second", Model: "a2"}}},
		"b": {Targets: []config.Target{{Provider: "fallback", Model: "b1"}}},
	}, map[string]string{"a": "b"}, false)
	r.brk.now = func() time.Time { return time.Unix(1, 0) }
	for _, name := range []string{"first", "fallback"} {
		for range 5 {
			r.RecordResult(name, name, false)
		}
	}
	got := r.DescribeModels(nil)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("configured catalog = %+v, want a, b", got)
	}
	var targets []string
	for _, target := range got[0].Targets {
		targets = append(targets, target.Model+"/"+target.ProviderName+"/"+target.Upstream)
	}
	if !reflect.DeepEqual(targets, []string{"a/first/a1", "a/second/a2", "b/fallback/b1"}) {
		t.Fatalf("configured priority/fallback order changed: %v", targets)
	}
	chain, _, err := r.ResolveChain("a")
	if err != nil || len(chain) != 1 || chain[0].ProviderName != "second" {
		t.Fatalf("runtime must still skip open targets: %+v, %v", chain, err)
	}
	for range 5 {
		r.RecordResult("second", "second", false)
	}
	chain, _, err = r.ResolveChain("a")
	if err != nil || !reflect.DeepEqual(chain, got[0].Targets) {
		t.Fatalf("all-open runtime recovery changed: %+v, %v", chain, err)
	}
}

func TestDescribeModelsCanonicalizesFallbackAliases(t *testing.T) {
	r := newTestRouterWithFallbacks(map[string]providers.Provider{
		"p": mockprovider.New("unused"),
	}, map[string]config.ModelConfig{
		"a": {Targets: []config.Target{{Provider: "p", Model: "a1"}}},
		"b": {Aliases: []string{"alias-b"}, Targets: []config.Target{{Provider: "p", Model: "b1"}}},
	}, map[string]string{"a": "alias-b"}, false)
	got := r.DescribeModels(&keystore.Principal{AllowedModels: []string{"a", "alias-b"}})
	if len(got) != 2 || len(got[0].Targets) != 2 || got[0].Targets[1].Model != "b" {
		t.Fatalf("authorized fallback alias was lost: %+v", got)
	}
	restricted := r.DescribeModels(&keystore.Principal{AllowedModels: []string{"a"}})
	if len(restricted) != 1 || len(restricted[0].Targets) != 1 || restricted[0].Targets[0].Model != "a" {
		t.Fatalf("fallback alias widened model permissions: %+v", restricted)
	}
}

func TestDescribeModelsPinsPermissionsAndProviderMetadataToSnapshot(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"p": {Type: "request-routing-anthropic", BaseURL: "https://old.invalid", Region: "eu", DataBoundary: "internal"},
		},
		Models: map[string]config.ModelConfig{
			"a": {Aliases: []string{"alias-a"}, ContextWindow: 32000, Capabilities: []string{"tools"}, Targets: []config.Target{{Provider: "p", Model: "old-a"}}},
			"b": {Aliases: []string{"alias-b"}, ContextWindow: 64000, Capabilities: []string{"vision"}, Targets: []config.Target{{Provider: "p", Model: "old-b"}}},
		},
		ModelFallbacks: map[string]string{"a": "b"},
	}
	old, _, err := live.BuildState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers["p"] = config.ProviderConfig{Type: "request-routing-openai_compatible", BaseURL: "https://new.invalid", Region: "us"}
	cfg.Models["a"] = config.ModelConfig{Aliases: []string{"alias-b"}, ContextWindow: 128000, Targets: []config.Target{{Provider: "p", Model: "new-a"}}}
	cfg.Models["b"] = config.ModelConfig{Aliases: []string{"alias-a"}, ContextWindow: 256000, Targets: []config.Target{{Provider: "p", Model: "new-b"}}}
	next, _, err := live.BuildState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	holder := &live.Holder{}
	holder.Swap(old)
	r := New(holder)
	r.SetPolicyGate(func(_ keystore.Principal, model string, canonical func(string) string) bool {
		holder.Swap(next) // force reload during discovery, without timing assumptions
		if canonical("alias-a") != "a" || canonical("alias-b") != "b" {
			t.Error("policy gate received another generation's aliases")
		}
		return model == canonical("alias-a") || model == canonical("alias-b")
	})
	got := r.DescribeModels(&keystore.Principal{
		AllowedModels: []string{"alias-a", "alias-b"}, TeamSnapshotLoaded: true,
		TeamSnapshot: &keystore.TeamRecord{AllowedRegions: []string{"eu"}},
	})
	if len(got) != 2 {
		t.Fatalf("catalog lost permissions during reload: %+v", got)
	}
	for i, want := range []struct {
		name    string
		window  int64
		cap     string
		targets []string
	}{
		{"a", 32000, "tools", []string{"old-a", "old-b"}},
		{"b", 64000, "vision", []string{"old-b"}},
	} {
		desc := got[i]
		if desc.Name != want.name || desc.ContextWindow != want.window || !reflect.DeepEqual(desc.Capabilities, []string{want.cap}) {
			t.Errorf("mixed model metadata: %+v", desc)
		}
		var upstreams []string
		for _, target := range desc.Targets {
			upstreams = append(upstreams, target.Upstream)
			if target.Provider.Name() != "anthropic" || target.Identity != "request-routing-anthropic\x00https://old.invalid" ||
				target.Region != "eu" || target.DataBoundary != "internal" {
				t.Errorf("mixed target generation: %+v", target)
			}
		}
		if !reflect.DeepEqual(upstreams, want.targets) {
			t.Errorf("mixed targets for %s: %v, want %v", want.name, upstreams, want.targets)
		}
	}
}

func TestDescribeModelsFiltersRegionsAcrossPrimaryAndFallback(t *testing.T) {
	st, _, err := live.BuildState(&config.Config{
		Providers: map[string]config.ProviderConfig{
			"eu":        {Type: "request-routing-anthropic", Region: "eu"},
			"us":        {Type: "request-routing-anthropic", Region: "us"},
			"unlabeled": {Type: "request-routing-anthropic"},
		},
		Models: map[string]config.ModelConfig{
			"a": {Targets: []config.Target{{Provider: "us", Model: "a-us"}, {Provider: "eu", Model: "a-eu"}, {Provider: "unlabeled", Model: "a-unknown"}}},
			"b": {Targets: []config.Target{{Provider: "us", Model: "b-us"}, {Provider: "eu", Model: "b-eu"}, {Provider: "unlabeled", Model: "b-unknown"}}},
		},
		ModelFallbacks: map[string]string{"a": "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	holder := &live.Holder{}
	holder.Swap(st)
	got := New(holder).DescribeModels(&keystore.Principal{
		AllowedModels: []string{"*"}, TeamSnapshotLoaded: true,
		TeamSnapshot: &keystore.TeamRecord{AllowedRegions: []string{"eu"}},
	})
	if len(got) != 2 || len(got[0].Targets) != 2 || len(got[1].Targets) != 1 {
		t.Fatalf("region filtering lost eligible models/targets: %+v", got)
	}
	for _, desc := range got {
		for _, target := range desc.Targets {
			if target.Region != "eu" || target.ProviderName != "eu" {
				t.Errorf("inaccessible region in descriptor: %+v", target)
			}
		}
	}
}
