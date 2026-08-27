package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig writes doc to a temp file and returns its path. A `{}` document
// loads cleanly through LoadRaw (verified by TestBudgetTimezone_defaultsToUTC),
// so each fixture below is just the keys under test.
func writeConfig(t *testing.T, doc string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestBudgetTimezone_defaultsToUTC(t *testing.T) {
	cfg, err := LoadRaw(writeConfig(t, `{}`))
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if cfg.BudgetLoc != time.UTC {
		t.Fatalf("BudgetLoc = %v, want time.UTC", cfg.BudgetLoc)
	}
	if cfg.BudgetLocation() != time.UTC {
		t.Fatalf("BudgetLocation() = %v, want time.UTC", cfg.BudgetLocation())
	}
}

func TestBudgetTimezone_resolvesNamedZone(t *testing.T) {
	cfg, err := LoadRaw(writeConfig(t, `{"budget_timezone":"Asia/Seoul"}`))
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if got := cfg.BudgetLocation().String(); got != "Asia/Seoul" {
		t.Fatalf("BudgetLocation() = %q, want %q", got, "Asia/Seoul")
	}
	// Assert the OFFSET, not just the name: a location that resolved to the
	// wrong zone data would still carry the right String(). KST is UTC+9, no
	// DST, so midnight Aug 25 in Seoul is 15:00Z the previous day.
	got := time.Date(2026, 8, 25, 0, 0, 0, 0, cfg.BudgetLocation())
	want := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("midnight Aug 25 KST = %v, want %v", got, want)
	}
}

func TestBudgetTimezone_unknownZoneIsLoadError(t *testing.T) {
	cfg, err := LoadRaw(writeConfig(t, `{"budget_timezone":"Mars/Olympus"}`))
	if err == nil {
		t.Fatalf("LoadRaw: want error for unknown zone, got nil")
	}
	if cfg != nil {
		t.Fatalf("LoadRaw: want nil config on error, got %+v", cfg)
	}
	if !strings.Contains(err.Error(), "budget_timezone") {
		t.Fatalf("error %q does not mention budget_timezone", err)
	}
}

func TestBudgetTimezone_localIsRefused(t *testing.T) {
	_, err := LoadRaw(writeConfig(t, `{"budget_timezone":"Local"}`))
	if err == nil {
		t.Fatalf(`LoadRaw: want error for "Local", got nil`)
	}
	if !strings.Contains(err.Error(), "budget_timezone") {
		t.Fatalf("error %q does not mention budget_timezone", err)
	}
}

func TestBudgetTimezone_explicitUTC(t *testing.T) {
	cfg, err := LoadRaw(writeConfig(t, `{"budget_timezone":"UTC"}`))
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if cfg.BudgetLoc != time.UTC {
		t.Fatalf("BudgetLoc = %v, want time.UTC", cfg.BudgetLoc)
	}
}

func TestBudgetConfig_usdPerDayParses(t *testing.T) {
	cfg, err := LoadRaw(writeConfig(t, `{
		"teams": {
			"demo": {"budget": {"usd_per_month": 1000, "usd_per_day": 50, "on_exceeded": "block"}}
		}
	}`))
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	b := cfg.Teams["demo"].Budget
	if b.USDPerMonth != 1000 {
		t.Fatalf("USDPerMonth = %v, want 1000", b.USDPerMonth)
	}
	if b.USDPerDay != 50 {
		t.Fatalf("USDPerDay = %v, want 50", b.USDPerDay)
	}
	if b.OnExceeded != "block" {
		t.Fatalf("OnExceeded = %q, want %q", b.OnExceeded, "block")
	}

	// INV-2's pin: with usd_per_day ABSENT, USDPerDay is 0 (not limited on
	// that dimension) and the monthly value is untouched.
	cfg, err = LoadRaw(writeConfig(t, `{
		"teams": {
			"demo": {"budget": {"usd_per_month": 1000, "on_exceeded": "block"}}
		}
	}`))
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	b = cfg.Teams["demo"].Budget
	if b.USDPerDay != 0 {
		t.Fatalf("USDPerDay = %v, want 0 when usd_per_day is absent", b.USDPerDay)
	}
	if b.USDPerMonth != 1000 {
		t.Fatalf("USDPerMonth = %v, want 1000 when usd_per_day is absent", b.USDPerMonth)
	}
}

func TestVirtualKeyBudgetUSDPerDay_negativeRejected(t *testing.T) {
	t.Setenv("TEST_VK_DAY_NEG", "ik_test_1234567890abcdef")
	_, err := LoadRaw(writeConfig(t, `{
		"virtual_keys": [
			{"team": "demo", "key_ref": {"env": "TEST_VK_DAY_NEG"}, "allowed_models": ["*"], "budget_usd_per_day": -1}
		]
	}`))
	if err == nil {
		t.Fatalf("LoadRaw: want error for negative budget_usd_per_day, got nil")
	}
	if !strings.Contains(err.Error(), "budget_usd_per_day") {
		t.Fatalf("error %q does not mention budget_usd_per_day", err)
	}
}

func TestVirtualKeyBudgetUSDPerDay_zeroAndPositiveAccepted(t *testing.T) {
	t.Setenv("TEST_VK_DAY_A", "ik_test_1234567890abcdef")
	t.Setenv("TEST_VK_DAY_B", "ik_test_fedcba0987654321")
	cfg, err := LoadRaw(writeConfig(t, `{
		"virtual_keys": [
			{"team": "demo", "key_ref": {"env": "TEST_VK_DAY_A"}, "allowed_models": ["*"], "budget_usd_per_day": 0},
			{"team": "demo", "key_ref": {"env": "TEST_VK_DAY_B"}, "allowed_models": ["*"], "budget_usd_per_day": 50.5}
		]
	}`))
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if got := cfg.VirtualKeys[0].BudgetUSDPerDay; got != 0 {
		t.Fatalf("virtual_keys[0].BudgetUSDPerDay = %v, want 0", got)
	}
	if got := cfg.VirtualKeys[1].BudgetUSDPerDay; got != 50.5 {
		t.Fatalf("virtual_keys[1].BudgetUSDPerDay = %v, want 50.5", got)
	}
}
