package configapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
)

func exportSnapshot() ExportDoc {
	return ExportDocFrom(
		map[string]config.ProviderConfig{
			"anthropic-prod": {
				Type:      "anthropic",
				BaseURL:   "https://api.anthropic.com",
				APIKeyRef: &config.SecretRef{Env: "ANTHROPIC_KEY"},
				APIKey:    "sk-RESOLVED-SECRET-must-not-export", // resolved value — must NOT be serialized
			},
		},
		map[string]config.ModelConfig{
			"claude": {Targets: []config.Target{{Provider: "anthropic-prod", Model: "claude-sonnet-4-6"}}},
		},
	)
}

func TestExportSecretFree(t *testing.T) {
	h := ExportHandler(exportSnapshot)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/config/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET export = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-RESOLVED-SECRET-must-not-export") {
		t.Fatalf("export leaked the resolved secret value: %s", body)
	}
	// The REF is exported (refs are non-secret operational data, ADR-005).
	if !strings.Contains(body, "ANTHROPIC_KEY") {
		t.Fatalf("export should carry the ref name: %s", body)
	}
}

func TestExportReParsesAsConfig(t *testing.T) {
	h := ExportHandler(exportSnapshot)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/config/export", nil))

	var cfg config.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("export does not re-parse as config: %v", err)
	}
	p, ok := cfg.Providers["anthropic-prod"]
	if !ok || p.Type != "anthropic" || p.APIKeyRef == nil || p.APIKeyRef.Env != "ANTHROPIC_KEY" {
		t.Fatalf("round-trip provider wrong: %+v", p)
	}
	if cfg.Models["claude"].Targets[0].Provider != "anthropic-prod" {
		t.Fatalf("round-trip model wrong: %+v", cfg.Models)
	}
}

// TestExportIncludesGuardrailFields pins export's genuinely zero-code-diff
// guardrail behavior against a future refactor: ExportDocFrom serializes
// config.ProviderConfig directly, which already carries the json-tagged
// GuardrailID/GuardrailVersion fields, so a DB-registered bedrock provider's
// guardrail must survive export → re-parse untouched (plan-gate round 1 finding).
func TestExportIncludesGuardrailFields(t *testing.T) {
	snapshot := func() ExportDoc {
		return ExportDocFrom(
			map[string]config.ProviderConfig{
				"bedrock-us": {Type: "bedrock", Region: "us-west-2", GuardrailID: "gr-abc", GuardrailVersion: "3"},
			},
			nil,
		)
	}
	h := ExportHandler(snapshot)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/config/export", nil))

	var cfg config.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("export does not re-parse as config: %v", err)
	}
	p, ok := cfg.Providers["bedrock-us"]
	if !ok || p.GuardrailID != "gr-abc" || p.GuardrailVersion != "3" {
		t.Fatalf("export lost guardrail fields: %+v", p)
	}
}

// TestExportPricingFragmentIsConfigShaped (ADR-041 item 6): the assembly-
// attached pricing fragment marshals as a config-shaped pricing block, so its
// overrides re-parse as config.PricingConfig overrides and paste directly into
// a file's pricing block — the DB→file migration path loses no rate.
func TestExportPricingFragmentIsConfigShaped(t *testing.T) {
	snapshot := func() ExportDoc {
		doc := ExportDocFrom(nil, nil)
		doc.Pricing = &PricingExport{Overrides: map[string]map[string]config.RateConfig{
			"gpu": {"glm-4.7": {InputPerMTok: 0.05, OutputPerMTok: 0.1}},
		}}
		return doc
	}
	h := ExportHandler(snapshot)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/config/export", nil))

	var cfg config.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("export does not re-parse as config: %v", err)
	}
	if got := cfg.Pricing.Overrides["gpu"]["glm-4.7"]; got.InputPerMTok != 0.05 || got.OutputPerMTok != 0.1 {
		t.Fatalf("pricing fragment did not round-trip through the config shape: %+v", got)
	}
}

// TestExportIncludesModelAliases mirrors TestExportIncludesGuardrailFields:
// ExportDocFrom serializes config.ModelConfig directly, which already carries
// the json-tagged Aliases field, so a model's aliases must survive
// export → re-parse untouched (ADR-021 follow-up).
func TestExportIncludesModelAliases(t *testing.T) {
	snapshot := func() ExportDoc {
		return ExportDocFrom(nil, map[string]config.ModelConfig{
			"claude": {Aliases: []string{"apac.claude"}, Targets: []config.Target{{Provider: "p", Model: "x"}}},
		})
	}
	h := ExportHandler(snapshot)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/config/export", nil))

	var cfg config.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("export does not re-parse as config: %v", err)
	}
	mc, ok := cfg.Models["claude"]
	if !ok || len(mc.Aliases) != 1 || mc.Aliases[0] != "apac.claude" {
		t.Fatalf("export lost model aliases: %+v", mc)
	}
}

func TestExportRejectsWrite(t *testing.T) {
	h := ExportHandler(exportSnapshot)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PUT", "/admin/config/export", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT export = %d, want 405", rec.Code)
	}
}
