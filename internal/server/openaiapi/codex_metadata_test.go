package openaiapi

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

type catalogProvider struct {
	providers.Provider
	name      string
	responses bool
}

func (p catalogProvider) Name() string { return p.name }
func (p catalogProvider) SupportsIngress(protocol string) bool {
	return p.responses && protocol == "responses"
}

func TestModelsAdvertiseCodexContractWithoutPrivateTargetNames(t *testing.T) {
	providers := map[string]providers.Provider{
		"native": catalogProvider{mockprovider.New("unused"), "openai_responses", true},
		"bridge": catalogProvider{mockprovider.New("unused"), "bedrock", true},
		"other":  catalogProvider{mockprovider.New("unused"), "custom", false},
	}
	models := map[string]config.ModelConfig{
		"openai.gpt-6-astra": {ContextWindow: 1000000, Capabilities: []string{"tools", "reasoning"}, Targets: []config.Target{{Provider: "native", Model: "private-deployment"}}},
		"grok":               {ContextWindow: 524288, Capabilities: []string{"tools"}, Targets: []config.Target{{Provider: "bridge", Model: "private-grok"}}},
		"text-only":          {ContextWindow: 32768, Targets: []config.Target{{Provider: "bridge", Model: "private-text"}}},
		"unsupported":        {Capabilities: []string{"tools"}, Targets: []config.Target{{Provider: "other", Model: "private-other"}}},
		"mixed":              {Capabilities: []string{"tools"}, Targets: []config.Target{{Provider: "native", Model: "private-deployment"}, {Provider: "bridge", Model: "private-grok"}}},
	}
	holder := &live.Holder{}
	holder.Swap(live.NewState(providers, models, pricing.New(pricing.OnMissingAllow, nil), nil))
	rec := httptest.NewRecorder()
	NewModelsHandler(router.New(holder)).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var response struct {
		Data []struct {
			ID           string   `json:"id"`
			Mode         string   `json:"responses_mode"`
			CodexModel   string   `json:"codex_model"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"openai.gpt-6-astra": "native", "grok": "bridge", "text-only": "bridge", "unsupported": "unsupported", "mixed": "bridge"}
	if len(response.Data) != len(want) {
		t.Fatalf("catalog returned %d models, want %d", len(response.Data), len(want))
	}
	for _, model := range response.Data {
		if model.Mode != want[model.ID] || model.Capabilities == nil {
			t.Errorf("incorrect client contract: %+v", model)
		}
		if model.ID == "openai.gpt-6-astra" {
			if model.CodexModel != "gpt-6-astra" {
				t.Errorf("wrong public catalog binding: %+v", model)
			}
		} else if model.CodexModel != "" {
			t.Errorf("bridge advertised native model metadata: %+v", model)
		}
	}
	if strings.Contains(rec.Body.String(), "private-") {
		t.Fatal("model discovery disclosed a private upstream deployment name")
	}
}

type catalogEntry struct {
	ID            string   `json:"id"`
	Mode          string   `json:"responses_mode"`
	CodexModel    string   `json:"codex_model"`
	Capabilities  []string `json:"capabilities"`
	ContextWindow int64    `json:"context_window"`
	MaxModelLen   int64    `json:"max_model_len"`
}

func readCatalog(t *testing.T, r *router.Router, p *keystore.Principal) []catalogEntry {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	if p != nil {
		req = req.WithContext(principal.With(req.Context(), *p))
	}
	rec := httptest.NewRecorder()
	NewModelsHandler(r).ServeHTTP(rec, req)
	var response struct {
		Data []catalogEntry `json:"data"`
	}
	if rec.Code != 200 {
		t.Fatalf("catalog status = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "private-") {
		t.Fatal("catalog disclosed a private upstream identifier")
	}
	return response.Data
}

func TestModelsContractIgnoresBreakerHealth(t *testing.T) {
	holder := &live.Holder{}
	holder.Swap(live.NewState(map[string]providers.Provider{
		"native": catalogProvider{mockprovider.New("unused"), "openai_responses", true},
		"bridge": catalogProvider{mockprovider.New("unused"), "bedrock", true},
	}, map[string]config.ModelConfig{
		"mixed": {ContextWindow: 32768, Capabilities: []string{"tools"}, Targets: []config.Target{
			{Provider: "bridge", Model: "private-bridge"}, {Provider: "native", Model: "private-native"},
		}},
	}, nil, map[string]string{"native": "native-id", "bridge": "bridge-id"}))
	r := router.New(holder)
	p := &keystore.Principal{AllowedModels: []string{"mixed"}}
	before := readCatalog(t, r, p)
	if len(before) != 1 || before[0].Mode != "bridge" {
		t.Fatalf("healthy mixed route = %+v, want bridge", before)
	}
	for range 10 {
		r.RecordResult("bridge", "bridge-id", false)
	}
	chain, _, err := r.ResolveChain("mixed")
	if err != nil || len(chain) != 1 || chain[0].ProviderName != "native" {
		t.Fatalf("inference must skip the open bridge: %+v, %v", chain, err)
	}
	if after := readCatalog(t, r, p); !reflect.DeepEqual(after, before) {
		t.Fatalf("breaker changed catalog: before=%+v after=%+v", before, after)
	}
}

func TestModelsContractScopesFallbackPermissions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		allowed       []string
		policyAllowed []string
		bridge        bool
		want          map[string]string
	}{
		{"key primary only", []string{"openai.modelA"}, nil, false, map[string]string{"openai.modelA": "native"}},
		{"alias primary only", []string{"aliasA"}, nil, false, map[string]string{"openai.modelA": "native"}},
		{"policy primary only", []string{"*"}, []string{"aliasA"}, false, map[string]string{"openai.modelA": "native"}},
		{"policy cannot widen key", []string{"aliasA"}, []string{"aliasA", "aliasB"}, false, map[string]string{"openai.modelA": "native"}},
		{"authorized unsupported fallback", []string{"aliasA", "aliasB"}, nil, false, map[string]string{"openai.modelA": "unsupported", "modelB": "unsupported"}},
		{"authorized bridge fallback", []string{"*"}, nil, true, map[string]string{"openai.modelA": "bridge", "modelB": "bridge"}},
		{"policy excludes primary", []string{"*"}, []string{"aliasB"}, false, map[string]string{"modelB": "unsupported"}},
		{"empty key cannot be widened", nil, []string{"aliasA", "aliasB"}, false, map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			holder := &live.Holder{}
			holder.Swap(live.NewStateWithFallbacks(map[string]providers.Provider{
				"native": catalogProvider{mockprovider.New("unused"), "openai_responses", true},
				"other":  catalogProvider{mockprovider.New("unused"), "custom", tc.bridge},
			}, map[string]config.ModelConfig{
				"openai.modelA": {Aliases: []string{"aliasA"}, Capabilities: []string{"tools"}, Targets: []config.Target{{Provider: "native", Model: "private-a"}}},
				"modelB":        {Aliases: []string{"aliasB"}, Capabilities: []string{"tools"}, Targets: []config.Target{{Provider: "other", Model: "private-b"}}},
			}, nil, nil, map[string]string{"openai.modelA": "modelB"}, false))
			r := router.New(holder)
			if tc.policyAllowed != nil {
				r.SetPolicyGate(func(_ keystore.Principal, model string, canonical func(string) string) bool {
					for _, name := range tc.policyAllowed {
						if canonical(name) == model {
							return true
						}
					}
					return false
				})
			}
			catalog := readCatalog(t, r, &keystore.Principal{AllowedModels: tc.allowed})
			if len(catalog) != len(tc.want) {
				t.Fatalf("catalog = %+v, want modes %v", catalog, tc.want)
			}
			for _, entry := range catalog {
				if mode, ok := tc.want[entry.ID]; !ok || entry.Mode != mode {
					t.Errorf("catalog = %+v, want modes %v", catalog, tc.want)
				}
				if entry.Mode == "native" {
					if entry.CodexModel != "modelA" {
						t.Errorf("native binding must use public name: %+v", entry)
					}
				} else if entry.CodexModel != "" {
					t.Errorf("non-native catalog has native binding: %+v", entry)
				}
			}
		})
	}
}

type reloadingCatalogProvider struct {
	catalogProvider
	reload func()
}

func (p reloadingCatalogProvider) SupportsIngress(protocol string) bool {
	p.reload()
	return p.catalogProvider.SupportsIngress(protocol)
}

func TestModelsContractUsesOneGenerationForWholeList(t *testing.T) {
	holder := &live.Holder{}
	next := live.NewState(map[string]providers.Provider{
		"p": catalogProvider{mockprovider.New("unused"), "custom", false},
	}, map[string]config.ModelConfig{
		"a": {ContextWindow: 128000, Capabilities: []string{"reasoning"}, Targets: []config.Target{{Provider: "p", Model: "private-new-a"}}},
		"b": {ContextWindow: 256000, Capabilities: []string{"vision"}, Targets: []config.Target{{Provider: "p", Model: "private-new-b"}}},
	}, nil, nil)
	holder.Swap(live.NewState(map[string]providers.Provider{
		"p": reloadingCatalogProvider{
			catalogProvider{mockprovider.New("unused"), "openai_responses", true},
			func() { holder.Swap(next) },
		},
	}, map[string]config.ModelConfig{
		"a": {ContextWindow: 32000, Capabilities: []string{"tools"}, Targets: []config.Target{{Provider: "p", Model: "private-old-a"}}},
		"b": {ContextWindow: 64000, Capabilities: []string{"tools"}, Targets: []config.Target{{Provider: "p", Model: "private-old-b"}}},
	}, nil, nil))
	got := readCatalog(t, router.New(holder), nil)
	want := []catalogEntry{
		{ID: "a", Mode: "native", CodexModel: "a", Capabilities: []string{"tools"}, ContextWindow: 32000, MaxModelLen: 32000},
		{ID: "b", Mode: "native", CodexModel: "b", Capabilities: []string{"tools"}, ContextWindow: 64000, MaxModelLen: 64000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog mixed topology generations: got=%+v want=%+v", got, want)
	}
}

func TestModelsContractLoadedRegionRestrictionRejectsUnlabeledTargets(t *testing.T) {
	holder := &live.Holder{}
	holder.Swap(live.NewState(map[string]providers.Provider{
		"native": catalogProvider{mockprovider.New("unused"), "openai_responses", true},
	}, map[string]config.ModelConfig{
		"m": {Targets: []config.Target{{Provider: "native", Model: "private-native"}}},
	}, nil, nil))
	r := router.New(holder)
	for _, tc := range []struct {
		name   string
		loaded bool
		team   *keystore.TeamRecord
		mode   string
	}{
		{"loaded restriction", true, &keystore.TeamRecord{AllowedRegions: []string{"eu"}}, "unsupported"},
		{"loaded unrestricted", true, &keystore.TeamRecord{}, "native"},
		{"loaded absent team", true, nil, "native"},
		{"unloaded snapshot is not evidence", false, &keystore.TeamRecord{AllowedRegions: []string{"eu"}}, "native"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readCatalog(t, r, &keystore.Principal{
				AllowedModels: []string{"m"}, TeamSnapshotLoaded: tc.loaded, TeamSnapshot: tc.team,
			})
			if len(got) != 1 || got[0].Mode != tc.mode {
				t.Fatalf("catalog = %+v, want %s", got, tc.mode)
			}
		})
	}
}
