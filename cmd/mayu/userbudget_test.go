package main

// E2E regression tests for the per-USER budget wiring (ADR-042 Phase 3): the
// gov.SetUserLookup closure in gateway.go that connects policy.Store.UserLimits
// to the governance pipeline, including its lossy collapse of the two windows'
// hardCap flags into UserPolicy's single on_exceeded knob (block wins on tie).
// The package layers on both sides are unit-tested; this assembly point is
// covered ONLY here, and its failure mode is silent — the limit is stored,
// reported by the API, and never enforced.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createOwnedKey is createKey (e2e_test.go) plus an "owner" field — per-user
// budget keys on Principal.Owner, which createKey never sets. The admin-API
// request field is "owner" (internal/server/adminapi/keys.go); the response
// fields are key_id and plaintext.
func createOwnedKey(t *testing.T, adminURL, team, owner string, models []string) (string, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"team": team, "allowed_models": models, "owner": owner})
	req, _ := http.NewRequest(http.MethodPost, adminURL+"/admin/keys", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e2eAdminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create key: status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		KeyID     string `json:"key_id"`
		Plaintext string `json:"plaintext"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("create key: decode: %v", err)
	}
	if out.KeyID == "" || !strings.HasPrefix(out.Plaintext, "ik_") {
		t.Fatalf("create key: unexpected payload key_id=%q plaintext_prefix=%q", out.KeyID, out.Plaintext[:min(len(out.Plaintext), 3)])
	}
	return out.KeyID, out.Plaintext
}

// userBudgetGateway boots a gateway with the shared pricing override (one
// settled request costs ~$15 — far past any milliUSD cap in this file), the
// given config teams (allowed_models "*", NO budget of their own, so every
// money cap in play comes from the policy document), and the given
// GovernancePolicy YAML on the local file channel.
func userBudgetGateway(t *testing.T, upstreamURL, policyYAML string, teams ...string) (dataURL, adminURL string) {
	t.Helper()
	dataURL, adminURL, _ = bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(upstreamURL)(cfg, dir)
		tm := map[string]any{}
		for _, team := range teams {
			tm[team] = map[string]any{"allowed_models": []any{"*"}}
		}
		cfg["teams"] = tm
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(polDir, "user.yaml"), []byte(policyYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})
	return dataURL, adminURL
}

// mustPost sends one Messages request and asserts the status, returning the
// body for message checks.
func mustPost(t *testing.T, dataURL, key string, wantStatus int, why string) string {
	t.Helper()
	r := postMessages(t, dataURL, key, "claude-test")
	got, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != wantStatus {
		t.Fatalf("%s: status %d: %s, want %d", why, r.StatusCode, got, wantStatus)
	}
	return string(got)
}

// TestE2EUserSubjectBudgetBlocksOneUserNotTheTeam is the headline of the
// phase: a user-ONLY budget policy caps the named user and NOBODY else on the
// team. The teammate's 200s are asserted AFTER the capped user's 402 on
// purpose — a per-team implementation debits the whole team on the capped
// user's spend, so this ordering is what makes it fail.
//
// failurePolicy: FailClosed + hardCap: true is load-bearing: a soft/FailOpen
// rule resolves to warn over the empty base, admits the request, and never
// produces a 402 — the test would assert nothing.
func TestE2EUserSubjectBudgetBlocksOneUserNotTheTeam(t *testing.T) {
	up := newAnthropicUpstream(t)
	pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-user-budget }
spec:
  subject: { user: sub-capped }
  rules:
  - name: user-month-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1, hardCap: true }
`
	dataURL, adminURL := userBudgetGateway(t, up.srv.URL, pol, "pol-team")

	_, cappedKey := createOwnedKey(t, adminURL, "pol-team", "sub-capped", []string{"*"})
	_, freeKey := createOwnedKey(t, adminURL, "pol-team", "sub-free", []string{"*"})

	// Pre-check runs on ACCUMULATED spend, so the capped user's first request
	// always gets through; the second is the one that is denied.
	mustPost(t, dataURL, cappedKey, http.StatusOK, "capped user's first request")
	body := mustPost(t, dataURL, cappedKey, http.StatusPaymentRequired, "capped user past the cap")
	if !strings.Contains(body, "user budget exceeded") {
		t.Fatalf("402 must name the USER budget: %s", body)
	}

	// THE assertion the phase exists for: the teammate is untouched, checked
	// after the 402 so a team-keyed debit cannot pass.
	mustPost(t, dataURL, freeKey, http.StatusOK, "teammate's first request after the 402")
	mustPost(t, dataURL, freeKey, http.StatusOK, "teammate's second request after the 402")

	// /v1/usage reports the per-user counter only for the user a policy
	// matched (omitempty on a lookup miss).
	if u := getUsage(t, dataURL, cappedKey); u["user_budget"] == nil {
		t.Fatalf("capped user's /v1/usage must report user_budget: %+v", u)
	}
	if u := getUsage(t, dataURL, freeKey); u["user_budget"] != nil {
		t.Fatalf("uncapped teammate's /v1/usage must NOT report user_budget: %+v", u)
	}
}

// TestE2EUserSubjectBudgetTeamScopedRuleDoesNotLeakToOtherTeams: a
// (team, user) subject binds that user IN THAT TEAM only — the same owner
// string on a key in another team is not capped.
func TestE2EUserSubjectBudgetTeamScopedRuleDoesNotLeakToOtherTeams(t *testing.T) {
	up := newAnthropicUpstream(t)
	pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-user-t1 }
spec:
  subject: { team: t1, user: sub-x }
  rules:
  - name: t1-user-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1, hardCap: true }
`
	dataURL, adminURL := userBudgetGateway(t, up.srv.URL, pol, "t1", "t2")

	_, t1Key := createOwnedKey(t, adminURL, "t1", "sub-x", []string{"*"})
	_, t2Key := createOwnedKey(t, adminURL, "t2", "sub-x", []string{"*"})

	mustPost(t, dataURL, t1Key, http.StatusOK, "sub-x in t1, first request")
	mustPost(t, dataURL, t1Key, http.StatusPaymentRequired, "sub-x in t1 past the cap")

	// The same user in t2 sails past the cap — the rule is scoped to t1.
	mustPost(t, dataURL, t2Key, http.StatusOK, "sub-x in t2, first request")
	mustPost(t, dataURL, t2Key, http.StatusOK, "sub-x in t2, second request (past the t1 cap)")
}

// TestE2EUserBudgetRuleCreatesNoTeamClamp is the anti-regression for the
// lease-channel exclusion (design §F): a user-only budget rule must not turn
// into a TEAM clamp. Each settled request here costs ~$15, so three teammate
// requests are thousands of times past the 1-milliUSD user cap — all three
// must still be 200.
func TestE2EUserBudgetRuleCreatesNoTeamClamp(t *testing.T) {
	up := newAnthropicUpstream(t)
	pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-user-budget }
spec:
  subject: { user: sub-capped }
  rules:
  - name: user-month-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1, hardCap: true }
`
	dataURL, adminURL := userBudgetGateway(t, up.srv.URL, pol, "pol-team")

	_, freeKey := createOwnedKey(t, adminURL, "pol-team", "sub-free", []string{"*"})

	for i := 1; i <= 3; i++ {
		mustPost(t, dataURL, freeKey, http.StatusOK, "teammate request under a user-only rule")
	}
}

// TestUserLookupWiringMapsPolicyLimitsToUserPolicy pins the collapse inside
// gateway.go's SetUserLookup closure: UserPolicy carries ONE on_exceeded knob
// for both windows, so the two per-window hardCap flags collapse there —
// block wins on tie. The closure is not reachable from package main (it is a
// literal inside newGateway and Governor.userLookup is unexported), so both
// halves are asserted through behaviour, per design §H item 4.
//
// Both scenarios breach only the DAY window (the month rule is roomy), and
// the day rule is SOFT in both. The only difference is the month rule's
// hardCap — so the day breach's outcome (402 vs admitted) is decided purely
// by the collapse. A per-window resolution would admit in both cases.
func TestUserLookupWiringMapsPolicyLimitsToUserPolicy(t *testing.T) {
	t.Run("SoftDayBesideHardMonthBlocks", func(t *testing.T) {
		up := newAnthropicUpstream(t)
		// 100_000 milliUSD = $100/month — far above anything this test
		// spends, so the 402 can only be the day window's.
		pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-user-mixed }
spec:
  subject: { user: sub-mixed }
  rules:
  - name: soft-day
    failurePolicy: FailOpen
    budget:
      period: CalendarDay
      limitMilliUSD: 1
  - name: hard-month
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 100000, hardCap: true }
`
		dataURL, adminURL := userBudgetGateway(t, up.srv.URL, pol, "pol-team")
		_, key := createOwnedKey(t, adminURL, "pol-team", "sub-mixed", []string{"*"})

		mustPost(t, dataURL, key, http.StatusOK, "first request")
		// The DAY window is breached and its own rule was soft — the 402
		// proves the hard month rule's hardCap collapsed into the single
		// knob (BudgetExceeded == "block").
		body := mustPost(t, dataURL, key, http.StatusPaymentRequired, "day breach beside a hard month rule")
		if !strings.Contains(body, "user daily budget exceeded") {
			t.Fatalf("402 must be the DAY window, not the roomy month cap: %s", body)
		}
	})

	t.Run("TwoSoftRulesWarnAndStillSettle", func(t *testing.T) {
		up := newAnthropicUpstream(t)
		pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-user-soft }
spec:
  subject: { user: sub-soft }
  rules:
  - name: soft-day
    failurePolicy: FailOpen
    budget:
      period: CalendarDay
      limitMilliUSD: 1
  - name: soft-month
    failurePolicy: FailOpen
    budget: { limitMilliUSD: 100000 }
`
		dataURL, adminURL := userBudgetGateway(t, up.srv.URL, pol, "pol-team")
		_, key := createOwnedKey(t, adminURL, "pol-team", "sub-soft", []string{"*"})

		mustPost(t, dataURL, key, http.StatusOK, "first request")
		// Two soft rules collapse to "warn": the over-cap request is
		// ADMITTED...
		mustPost(t, dataURL, key, http.StatusOK, "over-cap request under soft-only rules")
		// ...and still SETTLES: the user day counter keeps accumulating past
		// its limit rather than being dropped along with the block.
		u := getUsage(t, dataURL, key)
		day, ok := u["user_budget_day"].(map[string]any)
		if !ok {
			t.Fatalf("/v1/usage must report user_budget_day for the soft-capped user: %+v", u)
		}
		spent, _ := day["spent_usd_micros"].(float64)
		limit, _ := day["limit_usd_micros"].(float64)
		if spent <= limit {
			t.Fatalf("warn must still settle: spent %v µUSD, want > limit %v µUSD after two ~$15 requests", spent, limit)
		}
	})
}
