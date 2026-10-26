package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestRestrictiveBudgetPreservesHardAdmission(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		base, overlay, want      int64
		baseMode, mode, wantMode string
	}{
		{name: "omitted-base-block", base: 3_000, overlay: 1_000, want: 3_000, mode: "warn", wantMode: "block"},
		{name: "explicit-base-block", base: 3_000, overlay: 1_000, want: 3_000, baseMode: "block", mode: "warn", wantMode: "block"},
		{name: "hard-lease-wider-than-soft", base: 1_000, overlay: 3_000, want: 3_000, baseMode: "warn", mode: "block", wantMode: "block"},
		{name: "hard-lease-equal-soft", base: 1_000, overlay: 1_000, want: 1_000, baseMode: "warn", mode: "block", wantMode: "block"},
		{name: "hard-caps-narrow", base: 3_000, overlay: 2_000, want: 2_000, baseMode: "block", mode: "block", wantMode: "block"},
		{name: "soft-meters-narrow", base: 3_000, overlay: 1_000, want: 1_000, baseMode: "warn", mode: "warn", wantMode: "warn"},
		{name: "absent-base-default-does-not-harden-soft", overlay: 1_000, want: 1_000, mode: "warn", wantMode: "warn"},
		{name: "absent-overlay-preserves-base", base: 3_000, want: 3_000, baseMode: "warn", wantMode: "warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit, mode := restrictiveBudget(tc.base, tc.baseMode, tc.overlay, tc.mode)
			if limit != tc.want || mode != tc.wantMode {
				t.Fatalf("got limit=%d mode=%q, want limit=%d mode=%q", limit, mode, tc.want, tc.wantMode)
			}
		})
	}
}

// Exercise the installed lookup through usage: a smaller soft threshold must
// neither erase an independent hard cap nor become that cap's blocking limit.
func TestE2EBudgetOverlayPreservesIndependentHardLimit(t *testing.T) {
	for _, period := range []string{"CalendarMonth", "CalendarDay"} {
		for _, baseMode := range []string{"", "block", "warn"} {
			t.Run(period+"/"+baseMode, func(t *testing.T) {
				up := newAnthropicUpstream(t)
				dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
					teamsAPIConfig(up.srv.URL)(cfg, dir)
					dimension := "usd_per_month"
					if period == "CalendarDay" {
						dimension = "usd_per_day"
					}
					baseLimit, policyLimit, policyHard := 0.003, 1, false
					if baseMode == "warn" {
						baseLimit, policyLimit, policyHard = 0.001, 3, true
					}
					budget := map[string]any{dimension: baseLimit}
					if baseMode != "" {
						budget["on_exceeded"] = baseMode
					}
					cfg["teams"] = map[string]any{"overlay-team": map[string]any{
						"allowed_models": []any{"*"}, "budget": budget,
					}}
					document := fmt.Sprintf(`apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: {name: overlay-threshold}
spec:
  subject: {team: overlay-team}
  rules:
  - name: threshold
    failurePolicy: FailClosed
    budget: {period: %s, limitMilliUSD: %d, hardCap: %t}
`, period, policyLimit, policyHard)
					path := filepath.Join(dir, "overlay.yaml")
					if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
						t.Fatal(err)
					}
					cfg["policies"] = []any{path}
				})
				_, key := createKey(t, adminURL, "overlay-team", []string{"*"})
				window := "team_budget"
				if period == "CalendarDay" {
					window = "team_budget_day"
				}
				usage := getUsage(t, dataURL, key)
				if limit, ok := budgetLimitMicros(usage, window); !ok || limit != 3_000 {
					t.Fatalf("hard admission cap replaced by soft threshold: limit=%d found=%v usage=%+v", limit, ok, usage)
				}
				blocked := false
				for range 2 {
					resp := postMessages(t, dataURL, key, "claude-test")
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode == http.StatusPaymentRequired {
						blocked = true
						break
					}
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("request status=%d, want 200 or 402", resp.StatusCode)
					}
				}
				if !blocked {
					t.Fatal("independent hard cap became warn-only")
				}
			})
		}
	}
}
