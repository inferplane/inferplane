package providerstore

// ADR-041 item 6: target-carried nominal pricing — SQLite round-trip and
// migration, the (provider, upstream) fold with conflict detection, and the
// overlay merge into the effective config's pricing overrides.

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
)

func TestTargetPricingRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	targets := []Target{
		{Provider: "gpu", Model: "glm-4.7", Pricing: &TargetPricing{InputPerMTok: 0.05, OutputPerMTok: 0.1}},
		{Provider: "gpu", Model: "scratch-llm", Pricing: &TargetPricing{Free: true}},
		{Provider: "anthropic-prod", Model: "claude-sonnet-4-6"}, // no declared rate
	}
	if err := s.SetModel(ctx, "m", ModelRoute{Targets: targets}); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	got, err := s.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["m"].Targets, targets) {
		t.Fatalf("pricing not round-tripped:\n got %+v\nwant %+v", got["m"].Targets, targets)
	}
	if got["m"].Targets[2].Pricing != nil {
		t.Fatal("a target without a declared rate must read back Pricing == nil")
	}
}

// TestMigrationAddsPricingColumns: a model_targets table from before item 6
// migrates in place; pre-existing rows read back with no pricing.
func TestMigrationAddsPricingColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	const oldSchema = `
CREATE TABLE providers (
    name             TEXT PRIMARY KEY,
    type             TEXT NOT NULL,
    base_url         TEXT NOT NULL DEFAULT '',
    region           TEXT NOT NULL DEFAULT '',
    auth_mode        TEXT NOT NULL DEFAULT '',
    auth_profile     TEXT NOT NULL DEFAULT '',
    api_key_ref_env  TEXT NOT NULL DEFAULT '',
    api_key_ref_file TEXT NOT NULL DEFAULT '',
    auth_header       TEXT NOT NULL DEFAULT '',
    guardrail_id      TEXT NOT NULL DEFAULT '',
    guardrail_version TEXT NOT NULL DEFAULT ''
);
CREATE TABLE model_targets (model TEXT, position INTEGER, provider TEXT, model_id TEXT, api TEXT NOT NULL DEFAULT '', PRIMARY KEY (model, position));
CREATE TABLE model_aliases (model TEXT NOT NULL, alias TEXT PRIMARY KEY);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`
	if _, err := old.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO model_targets (model, position, provider, model_id, api) VALUES ('pre', 0, 'p', 'u', '')`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite on pre-migration schema: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	got, err := s.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels on migrated table: %v", err)
	}
	if len(got["pre"].Targets) != 1 || got["pre"].Targets[0].Pricing != nil {
		t.Fatalf("migrated row must read back with nil pricing: %+v", got["pre"].Targets)
	}
	if err := s.SetModel(ctx, "new", ModelRoute{Targets: []Target{
		{Provider: "p", Model: "u2", Pricing: &TargetPricing{InputPerMTok: 1, OutputPerMTok: 2}},
	}}); err != nil {
		t.Fatalf("SetModel after migration: %v", err)
	}
	got, _ = s.ListModels(ctx)
	if p := got["new"].Targets[0].Pricing; p == nil || p.InputPerMTok != 1 {
		t.Fatalf("post-migration pricing write: %+v", got["new"].Targets)
	}
}

func TestPricingOverridesFoldAndConflicts(t *testing.T) {
	rate := &TargetPricing{InputPerMTok: 1, OutputPerMTok: 2}
	models := map[string]ModelRoute{
		"a": {Targets: []Target{{Provider: "gpu", Model: "x", Pricing: rate}}},
		// Same key, SAME rate from another route — agreement, not a conflict.
		"b": {Targets: []Target{{Provider: "gpu", Model: "x", Pricing: &TargetPricing{InputPerMTok: 1, OutputPerMTok: 2}}}},
		// Different key.
		"c": {Targets: []Target{{Provider: "gpu", Model: "y", Pricing: &TargetPricing{Free: true}}}},
		// No declaration at all.
		"d": {Targets: []Target{{Provider: "gpu", Model: "x"}}},
	}
	folded, conflicts := PricingOverrides(models)
	if len(conflicts) != 0 {
		t.Fatalf("agreeing declarations must not conflict: %v", conflicts)
	}
	if got := folded["gpu"]["x"]; got != *rate {
		t.Fatalf("fold: got %+v", got)
	}
	if !folded["gpu"]["y"].Free {
		t.Fatalf("free row lost in fold: %+v", folded["gpu"])
	}

	// A disagreeing declaration for the same (provider, upstream) key conflicts,
	// and the fold stays deterministic (sorted model name — "a" wins).
	models["b"] = ModelRoute{Targets: []Target{{Provider: "gpu", Model: "x", Pricing: &TargetPricing{InputPerMTok: 9, OutputPerMTok: 9}}}}
	folded, conflicts = PricingOverrides(models)
	if len(conflicts) != 1 || !strings.Contains(conflicts[0], `"a"`) || !strings.Contains(conflicts[0], `"b"`) {
		t.Fatalf("want one conflict naming both models, got %v", conflicts)
	}
	if got := folded["gpu"]["x"]; got != *rate {
		t.Fatalf("fold must be deterministic (first declaration in sorted order wins): %+v", got)
	}
}

// TestOverlayMergesTargetPricing: the money path — a DB-carried rate lands in
// the effective config's pricing overrides keyed (provider, upstream), a DB
// rate wins over a file override for the same key, file overrides for other
// keys survive, and the RAW file config is never mutated (eff is copy-on-write).
func TestOverlayMergesTargetPricing(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.UpsertProvider(ctx, ProviderRow{Name: "gpu", Type: "openai_compatible", BaseURL: "https://vllm", APIKeyRefEnv: "K"})
	_ = s.SetModel(ctx, "glm", ModelRoute{Targets: []Target{
		{Provider: "gpu", Model: "glm-4.7", Pricing: &TargetPricing{InputPerMTok: 0.05, OutputPerMTok: 0.1}},
	}})

	raw := fileCfg()
	raw.Pricing.Overrides = map[string]map[string]config.RateConfig{
		"gpu":   {"glm-4.7": {InputPerMTok: 99, OutputPerMTok: 99}, "other": {InputPerMTok: 3, OutputPerMTok: 4}},
		"file2": {"m": {Free: true}},
	}

	eff, err := Overlay(raw, s)
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}
	if got := eff.Pricing.Overrides["gpu"]["glm-4.7"]; got.InputPerMTok != 0.05 || got.OutputPerMTok != 0.1 {
		t.Fatalf("DB rate must win over the file override for its key: %+v", got)
	}
	if got := eff.Pricing.Overrides["gpu"]["other"]; got.InputPerMTok != 3 {
		t.Fatalf("file override for an untouched key must survive: %+v", got)
	}
	if !eff.Pricing.Overrides["file2"]["m"].Free {
		t.Fatal("file override for an untouched provider must survive")
	}
	if raw.Pricing.Overrides["gpu"]["glm-4.7"].InputPerMTok != 99 {
		t.Fatalf("the RAW file config must never be mutated: %+v", raw.Pricing.Overrides["gpu"]["glm-4.7"])
	}
}

// TestOverlayNoDBRatesLeavesPricingUntouched: with no target-carried rate the
// effective pricing block is the file's own, map identity included — the merge
// is a strict no-op, so a store without rates cannot perturb file pricing.
func TestOverlayNoDBRatesLeavesPricingUntouched(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.UpsertProvider(ctx, ProviderRow{Name: "gpu", Type: "openai_compatible", BaseURL: "https://vllm", APIKeyRefEnv: "K"})
	_ = s.SetModel(ctx, "glm", ModelRoute{Targets: []Target{{Provider: "gpu", Model: "glm-4.7"}}})

	raw := fileCfg()
	raw.Pricing.Overrides = map[string]map[string]config.RateConfig{"gpu": {"glm-4.7": {InputPerMTok: 1, OutputPerMTok: 2}}}
	eff, err := Overlay(raw, s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(eff.Pricing.Overrides, raw.Pricing.Overrides) {
		t.Fatalf("no DB rates ⇒ pricing must be untouched: %+v", eff.Pricing.Overrides)
	}
}
