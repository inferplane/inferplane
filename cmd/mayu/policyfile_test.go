package main

// E2E tests for the local GovernancePolicy file channel (ADR-033): documents
// from the config "policies" key are enforced live, and a policy file
// contributes ONLY the dimensions its rules declare — the review-finding
// regression where a modelAccess-only policy manufactured an "unlimited"
// team entry that shadowed the config budget.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const policyFileYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-team-models }
spec:
  subject: { team: pol-team }
  rules:
  - name: test-model-only
    failurePolicy: FailOpen
    modelAccess: { allow: ["claude-test"] }
`

// A modelAccess-only policy must (a) enforce the model allow-list and
// (b) leave the team's CONFIG budget fully in force — file dimensions
// overlay, they never replace the whole team policy.
func TestE2EPolicyFileModelAccessOverlaysConfigBudget(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		// Second configured model so the policy has something to deny.
		cfg["models"].(map[string]any)["claude-other"] = map[string]any{
			"targets": []any{map[string]any{"provider": "up", "model": "claude-other"}},
		}
		cfg["pricing"].(map[string]any)["overrides"].(map[string]any)["up"].(map[string]any)["claude-other"] = map[string]any{"input_per_mtok": 1000000.0, "output_per_mtok": 1000000.0}
		// Config team budget under reserve/settle economics (see govConfig in
		// e2e_test.go): $37 upper bound per request, $15 settled — $40 admits
		// the first request and blocks the second.
		cfg["teams"] = map[string]any{
			"pol-team": map[string]any{
				"allowed_models": []any{"*"},
				"budget":         map[string]any{"usd_per_month": 40.0, "on_exceeded": "block"},
			},
		}
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(polDir, "team.yaml"), []byte(policyFileYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})

	_, key := createKey(t, adminURL, "pol-team", []string{"*"})

	// (a) modelAccess enforces: the policy-denied model 403s even though the
	// key's own allow-list is a wildcard.
	r := postMessages(t, dataURL, key, "claude-other")
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("policy-denied model: status %d, want 403", r.StatusCode)
	}

	// (b) the config budget survives the overlay: first allowed request
	// settles past the tiny limit, second blocks with 402. Before the fix,
	// the modelAccess-only policy manufactured an unlimited team entry and
	// this second request sailed through.
	r1 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first allowed request: status %d, want 200", r1.StatusCode)
	}
	r2 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("request after budget exhausted: status %d, want 402 (config budget must survive a modelAccess-only policy)", r2.StatusCode)
	}
}

// A policy budget rule DOES replace the budget dimension (file wins where it
// speaks) while leaving other dimensions to the base.
func TestE2EPolicyFileBudgetOverridesConfig(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		// Config says a roomy budget; the policy file caps it at 1 milliUSD.
		cfg["teams"] = map[string]any{
			"pol-team": map[string]any{
				"allowed_models": []any{"*"},
				"budget":         map[string]any{"usd_per_month": 1000.0, "on_exceeded": "block"},
			},
		}
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-team-cap }
spec:
  subject: { team: pol-team }
  rules:
  - name: hard-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 40000, hardCap: true }
`
		if err := os.WriteFile(filepath.Join(polDir, "cap.yaml"), []byte(pol), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})

	_, key := createKey(t, adminURL, "pol-team", []string{"*"})

	// First request reserves within the $40 file cap and settles $15; the
	// second's $37 bound no longer fits — the file budget bound, not the
	// config's $1000 (reserve/settle economics, see govConfig in e2e_test.go).
	r1 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first request: status %d, want 200", r1.StatusCode)
	}
	r2 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("second request: status %d, want 402 (file budget must bind)", r2.StatusCode)
	}
}

// PR #50 review finding (HIGH): a SOFT policy budget layered on a config
// team whose budget blocks must NOT loosen enforcement to warn — block wins
// on tie. Before the fix, the overlay unconditionally reset on_exceeded to
// the policy's own mode, so a tighter-but-soft file rule silently disabled
// blocking.
func TestE2EPolicyFileSoftBudgetKeepsBaseBlock(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		// Config: roomy budget, but BLOCK on exceed.
		cfg["teams"] = map[string]any{
			"pol-team": map[string]any{
				"allowed_models": []any{"*"},
				"budget":         map[string]any{"usd_per_month": 1000.0, "on_exceeded": "block"},
			},
		}
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		// Policy: tighter budget, SOFT (no hardCap) — must inherit block.
		pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-team-soft-cap }
spec:
  subject: { team: pol-team }
  rules:
  - name: soft-cap
    failurePolicy: FailOpen
    budget: { limitMilliUSD: 40000 }
`
		if err := os.WriteFile(filepath.Join(polDir, "cap.yaml"), []byte(pol), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})

	_, key := createKey(t, adminURL, "pol-team", []string{"*"})

	r1 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first request: status %d, want 200", r1.StatusCode)
	}
	// The second request's bound no longer fits the $40 file limit. warn
	// would admit (200); the base's block must win → 402.
	r2 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("second request: status %d, want 402 (soft policy budget must not downgrade base block to warn)", r2.StatusCode)
	}
}

// TestE2EPolicyFileModelAccessPreservesConfigDailyBudget is the day-window twin
// of TestE2EPolicyFileModelAccessOverlaysConfigBudget, and it guards the same
// class of bug that test was written for: a policy file that speaks about ONE
// dimension must not silently unlimit the others.
//
// A modelAccess-only policy declares no budget rule in either window, so
// cmd/mayu's overlay seeds the daily cap from the base layer and leaves it
// untouched. Nothing else covers that passthrough — the overlay closure
// only runs when a policy source is configured, so every other daily-budget
// test in this package returns before reaching it.
func TestE2EPolicyFileModelAccessPreservesConfigDailyBudget(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		// A config team whose ONLY money cap is the DAILY one, so a 402 can
		// come from nowhere else.
		cfg["teams"] = map[string]any{
			"pol-team": map[string]any{
				"allowed_models": []any{"*"},
				"budget":         map[string]any{"usd_per_day": 40.0, "on_exceeded": "block"},
			},
		}
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(polDir, "team.yaml"), []byte(policyFileYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})

	_, key := createKey(t, adminURL, "pol-team", []string{"*"})

	r1 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first allowed request: status %d, want 200", r1.StatusCode)
	}
	r2 := postMessages(t, dataURL, key, "claude-test")
	got, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("request after the daily budget was exhausted: status %d: %s, want 402 (the config DAILY budget must survive a modelAccess-only policy overlay)", r2.StatusCode, got)
	}
	if !strings.Contains(string(got), "daily budget exceeded") {
		t.Fatalf("402 must name the DAILY window: %s", got)
	}
}

// TestE2EPolicyFileBudgetRuleKeepsConfigDailyBudget reaches the ONE line that
// TestE2EPolicyFileModelAccessPreservesConfigDailyBudget cannot: the overlay
// closure's PolicyFromLimits literal. A modelAccess-only policy makes
// TeamLimits report no entry, so that closure returns the base early and never
// builds a new TeamPolicy. A policy that DOES declare a budget rule takes the
// other branch — and there the daily cap has to be copied across explicitly.
//
// The scenario is a real one for anyone on the local policy channel today: a
// GovernancePolicy budget rule with no period caps the MONTH (an omitted
// period means CalendarMonth), while the operator's config file caps the DAY.
// Both must bind: a month-only policy must leave the config's daily cap in
// force — the fall-through the per-window overlay must not break. The policy's
// monthly cap here is roomy, so the 402 can only be the daily one.
func TestE2EPolicyFileBudgetRuleKeepsConfigDailyBudget(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		cfg["teams"] = map[string]any{
			"pol-team": map[string]any{
				"allowed_models": []any{"*"},
				"budget":         map[string]any{"usd_per_day": 40.0, "on_exceeded": "block"},
			},
		}
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		// 100_000 milliUSD = $100/month — far above anything this test spends.
		pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-team-monthly }
spec:
  subject: { team: pol-team }
  rules:
  - name: roomy-monthly
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 100000, hardCap: true }
`
		if err := os.WriteFile(filepath.Join(polDir, "monthly.yaml"), []byte(pol), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})

	_, key := createKey(t, adminURL, "pol-team", []string{"*"})

	r1 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first request: status %d, want 200", r1.StatusCode)
	}
	r2 := postMessages(t, dataURL, key, "claude-test")
	got, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("second request: status %d: %s, want 402 (the config DAILY cap must survive a policy BUDGET rule)", r2.StatusCode, got)
	}
	if !strings.Contains(string(got), "daily budget exceeded") {
		t.Fatalf("402 must be the DAILY window, not the roomy policy month cap: %s", got)
	}
}

// TestE2EPolicyFileDayBudgetRuleBindsDailyWindow proves the policy channel now
// binds the DAY window: a period: CalendarDay budget rule with a tiny limit
// folds into TeamLimits.BudgetMicrosPerDay and is enforced by the overlay's
// day twin. The same document carries a ROOMY monthly rule, so the 402 can
// only be the daily one — the same isolation trick as
// TestE2EPolicyFileBudgetRuleKeepsConfigDailyBudget, mirrored.
func TestE2EPolicyFileDayBudgetRuleBindsDailyWindow(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		// Config team with NO money cap of its own: every cap in play comes
		// from the policy document.
		cfg["teams"] = map[string]any{
			"pol-team": map[string]any{
				"allowed_models": []any{"*"},
			},
		}
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		// 40_000 milliUSD/day admits one request ($37 bound) and blocks the
		// second ($15 settled + $37 bound); 100_000 milliUSD = $100/month is
		// far above anything this test spends.
		pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-team-day }
spec:
  subject: { team: pol-team }
  rules:
  - name: tiny-daily
    failurePolicy: FailClosed
    budget:
      period: CalendarDay
      limitMilliUSD: 40000
      hardCap: true
  - name: roomy-monthly
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 100000, hardCap: true }
`
		if err := os.WriteFile(filepath.Join(polDir, "day.yaml"), []byte(pol), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})

	_, key := createKey(t, adminURL, "pol-team", []string{"*"})

	r1 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first request: status %d, want 200", r1.StatusCode)
	}
	r2 := postMessages(t, dataURL, key, "claude-test")
	got, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("second request: status %d: %s, want 402 (a period: CalendarDay policy rule must bind the DAY window)", r2.StatusCode, got)
	}
	if !strings.Contains(string(got), "daily budget exceeded") {
		t.Fatalf("402 must be the DAILY window, not the roomy policy month cap: %s", got)
	}
}

// TestE2EPolicyFileSoftDayBudgetKeepsBaseBlock is the day-window twin of
// TestE2EPolicyFileSoftBudgetKeepsBaseBlock: a SOFT policy day rule layered
// on a config team whose DAILY budget blocks must not loosen enforcement to
// warn — block wins on tie, resolved per window. This pins the overlay's
// `tl.BudgetDayHard || base.BudgetDayExceeded == "block"` line, the kind of
// one-word inversion that would silently turn a hard cap into a warning.
func TestE2EPolicyFileSoftDayBudgetKeepsBaseBlock(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		// Config: roomy DAILY budget, but BLOCK on exceed.
		cfg["teams"] = map[string]any{
			"pol-team": map[string]any{
				"allowed_models": []any{"*"},
				"budget":         map[string]any{"usd_per_day": 1000.0, "on_exceeded": "block"},
			},
		}
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		// Policy: tighter DAY budget, SOFT (no hardCap) — must inherit block.
		pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-team-soft-day }
spec:
  subject: { team: pol-team }
  rules:
  - name: soft-day-cap
    failurePolicy: FailOpen
    budget:
      period: CalendarDay
      limitMilliUSD: 40000
`
		if err := os.WriteFile(filepath.Join(polDir, "day.yaml"), []byte(pol), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})

	_, key := createKey(t, adminURL, "pol-team", []string{"*"})

	r1 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first request: status %d, want 200", r1.StatusCode)
	}
	// The second request's bound no longer fits the $40 file limit. warn
	// would admit (200); the base's block must win → 402.
	r2 := postMessages(t, dataURL, key, "claude-test")
	got, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("second request: status %d: %s, want 402 (a soft policy DAY rule must not downgrade the base's block to warn)", r2.StatusCode, got)
	}
	if !strings.Contains(string(got), "daily budget exceeded") {
		t.Fatalf("402 must be the DAILY window: %s", got)
	}
}
