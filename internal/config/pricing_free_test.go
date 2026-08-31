package config

import (
	"strings"
	"testing"
)

// ADR-030 zero-rate hole. An override of `{"input_per_mtok": 0,
// "output_per_mtok": 0}` used to load cleanly, pass `mayu pricing check`, pass
// `on_missing: "block"` boot validation and settle at 0 uUSD with
// missing=false — indistinguishable in the audit record from a genuinely free
// rate. 0 means UNPRICED; genuinely free needs an explicit `"free": true`.
//
// Rejected at LOAD, not warned about, matching the precedent set by an
// unrecognized `pricing.on_missing` value and by an unknown `budget_timezone`:
// a money control that is silently wrong is worse than a refused boot.
//
// writeConfig lives in budget_timezone_test.go (same package) — do not redefine it.
func TestPricingOverride_zeroRateWithoutFreeIsLoadError(t *testing.T) {
	_, err := LoadRaw(writeConfig(t, `{"pricing":{"overrides":{"bedrock-apne1":{"zai.glm-5":{"input_per_mtok":0,"output_per_mtok":0}}}}}`))
	if err == nil {
		t.Fatal("LoadRaw: want an error for a 0/0 override, got nil")
	}
	for _, want := range []string{"bedrock-apne1", "zai.glm-5", "0 means unpriced, not free", "free"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must contain %q — it has to name the offending override and say what to do", err, want)
		}
	}
}

// The genuinely-free case stays expressible, by explicit opt-in only.
func TestPricingOverride_zeroRateWithFreeLoads(t *testing.T) {
	cfg, err := LoadRaw(writeConfig(t, `{"pricing":{"overrides":{"p":{"m":{"input_per_mtok":0,"output_per_mtok":0,"free":true}}}}}`))
	if err != nil {
		t.Fatalf(`LoadRaw: "free": true must load, got %v`, err)
	}
	if !cfg.Pricing.Overrides["p"]["m"].Free {
		t.Error("RateConfig.Free = false — the JSON key must unmarshal into the field")
	}
}

// A single-sided zero is unusual but not provably wrong, so it must still load.
func TestPricingOverride_singleSidedZeroLoads(t *testing.T) {
	for name, doc := range map[string]string{
		"input zero":  `{"pricing":{"overrides":{"p":{"m":{"input_per_mtok":0,"output_per_mtok":15}}}}}`,
		"output zero": `{"pricing":{"overrides":{"p":{"m":{"input_per_mtok":3,"output_per_mtok":0}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadRaw(writeConfig(t, doc)); err != nil {
				t.Errorf("LoadRaw: only BOTH rates being zero is an error, got %v", err)
			}
		})
	}
}

// An override with no rate keys at all is the same defect written differently:
// the zero VALUE is what makes it unpriced, not whether the key was typed.
func TestPricingOverride_emptyRateObjectIsLoadError(t *testing.T) {
	if _, err := LoadRaw(writeConfig(t, `{"pricing":{"overrides":{"p":{"m":{}}}}}`)); err == nil {
		t.Fatal("LoadRaw: want an error for an override with no rates, got nil")
	}
}
