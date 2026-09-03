package main

// Regression tests for the gateway WIRING of the per-window budget path
// (design §C12). After the daily-budget phase landed, mutation testing showed
// every package-level piece (LeaseTable, Syncer, policy store, governor) was
// pinned but the assembly point in gateway.go was not: swapping the two
// clamp(...) period arguments (G3), disabling the ADR-034 lease clamp
// entirely (G3b — a pre-existing gap), making the Syncer.SpentOf closure
// window-blind (G4), and deleting the day→month adminContact fallback (G5)
// all survived the whole suite. Each assertion below exists to kill one of
// those specific mutations — none of them re-tests what the package-level
// suites already pin.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/controlplane"
	"github.com/inferplane/inferplane/internal/policy"
)

// budgetLimitMicros digs usage[field]["limit_usd_micros"] out of a decoded
// /v1/usage body. Every JSON number arrives as float64, so the casts are
// checked: a missing key or a shape change must read as "not there", never
// silently compare equal to zero and make the caller's assertion vacuous.
func budgetLimitMicros(usage map[string]any, field string) (int64, bool) {
	b, ok := usage[field].(map[string]any)
	if !ok {
		return 0, false
	}
	v, ok := b["limit_usd_micros"].(float64)
	if !ok {
		return 0, false
	}
	return int64(v), true
}

// The two limits differ 20× on purpose, so their ADR-034 default grants
// (0.1% of the limit) differ 20× too: at zero spend the DAY allowance is
// 50 milliUSD = 50_000 µUSD and the MONTH allowance is 1000 milliUSD =
// 1_000_000 µUSD. Two distinct clamped numbers are what let an assertion
// tell the windows apart — a single shared allowance could never detect a
// swap.
const leaseWindowPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: lease-windows }
spec:
  subject: { team: lw-team }
  rules:
  - name: daily
    failurePolicy: FailOpen
    budget:
      period: CalendarDay
      limitMilliUSD: 50000
      lease: { renewInterval: "1s" }
  - name: monthly
    failurePolicy: FailOpen
    budget:
      limitMilliUSD: 1000000
      lease: { renewInterval: "1s" }
`

// TestE2ELeaseClampPerWindow kills G3 and G3b: each budget window's local
// limit must be clamped to its OWN lease allowance. /v1/usage reports the
// clamped limits because UsageOf reads the same TeamPolicy the clamp
// produced, so the two allowances are observable end to end.
func TestE2ELeaseClampPerWindow(t *testing.T) {
	t.Setenv("LW_CP_TOKEN", "lw-tok")

	// Real control plane on a test listener, exactly like
	// TestE2EControlPlaneDistributesAndEnforces.
	polDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(leaseWindowPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer("lw-tok", polDir)
	if err != nil {
		t.Fatal(err)
	}
	cpMux := http.NewServeMux()
	cp.Mount(cpMux)
	cpSrv := httptest.NewServer(cpMux)
	defer cpSrv.Close()

	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		cfg["control_plane"] = map[string]any{
			"url":       cpSrv.URL,
			"token_ref": map[string]any{"env": "LW_CP_TOKEN"},
			"dataplane": "lw-dp",
		}
	})

	_, key := createKey(t, adminURL, "lw-team", []string{"*"})

	const (
		dayLimitMicros   = int64(50_000_000)    // limitMilliUSD 50000 = $50/day
		monthLimitMicros = int64(1_000_000_000) // limitMilliUSD 1000000 = $1000/month
		dayAllowance     = int64(50_000)        // 0.1% of $50, at zero spend
		monthAllowance   = int64(1_000_000)     // 0.1% of $1000, at zero spend
	)

	// The first heartbeat (policy + lease grants) is asynchronous: poll
	// until BOTH windows exist and BOTH clamps have bitten (limit below the
	// rule's own raw limit). A swap (G3) still clamps both windows — just to
	// the wrong allowances — so it passes this gate and fails the exact
	// assertion below; a disabled clamp (G3b) leaves the raw limits in place
	// and times out here.
	deadline := time.Now().Add(5 * time.Second)
	var usage map[string]any
	for {
		usage = getUsage(t, dataURL, key)
		day, dayOK := budgetLimitMicros(usage, "team_budget_day")
		month, monthOK := budgetLimitMicros(usage, "team_budget")
		if dayOK && monthOK && day < dayLimitMicros && month < monthLimitMicros {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease clamp never bit either window — a disabled ADR-034 clamp block (G3b) leaves the raw limits in force; last /v1/usage: %+v", usage)
		}
		time.Sleep(50 * time.Millisecond)
	}

	day, ok := budgetLimitMicros(usage, "team_budget_day")
	if !ok {
		t.Fatalf("team_budget_day missing or malformed after the clamp bit: %+v", usage)
	}
	month, ok := budgetLimitMicros(usage, "team_budget")
	if !ok {
		t.Fatalf("team_budget missing or malformed after the clamp bit: %+v", usage)
	}
	// Both numbers, individually: asserting only one cannot detect the two
	// clamp(...) period arguments being crossed (G3), because each window
	// would still show *a* clamped value — just the other window's.
	if day != dayAllowance {
		t.Fatalf("DAY window clamped to %d µUSD, want the DAILY allowance %d (got the monthly one? clamp periods crossed, G3); full usage: %+v", day, dayAllowance, usage)
	}
	if month != monthAllowance {
		t.Fatalf("MONTH window clamped to %d µUSD, want the MONTHLY allowance %d (got the daily one? clamp periods crossed, G3); full usage: %+v", month, monthAllowance, usage)
	}
}

// A team whose ONLY money cap is the DAILY one. That is what makes G4
// observable at all: in one process every Settle debits the day and month
// counters by the same cost, so their VALUES can never tell a window-blind
// SpentOf from a correct one — but UsageOf leaves TeamBudget nil when the
// team has no month limit, so a closure that ignores its period argument and
// reads the month view reports 0 forever.
const dayOnlyReportPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: day-only-report }
spec:
  subject: { team: ds-team }
  rules:
  - name: daily-only
    failurePolicy: FailOpen
    budget:
      period: CalendarDay
      limitMilliUSD: 1000000
      lease: { renewInterval: "1s" }
`

// TestE2EDailyRuleReportsDailySpend kills G4: the gateway's Syncer.SpentOf
// closure must answer for the rule's OWN window, observed through the
// consumption reports the data plane actually sends upstream.
func TestE2EDailyRuleReportsDailySpend(t *testing.T) {
	t.Setenv("DS_CP_TOKEN", "ds-tok")

	polDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(dayOnlyReportPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer("ds-tok", polDir)
	if err != nil {
		t.Fatal(err)
	}
	cpMux := http.NewServeMux()
	cp.Mount(cpMux)

	// Record what the data plane reports by wrapping the REAL control
	// plane's mux rather than reimplementing one. The heartbeat loop appends
	// from its own goroutine while the test body reads, and this suite runs
	// under -race: both sides go through the mutex, always.
	var mu sync.Mutex
	var reports []policy.ConsumptionReport
	cpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1alpha1/sync" {
			body, _ := io.ReadAll(r.Body)
			var req policy.SyncRequest
			if json.Unmarshal(body, &req) == nil {
				mu.Lock()
				reports = append(reports, req.Reports...)
				mu.Unlock()
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		cpMux.ServeHTTP(w, r)
	}))
	defer cpSrv.Close()

	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		cfg["control_plane"] = map[string]any{
			"url":       cpSrv.URL,
			"token_ref": map[string]any{"env": "DS_CP_TOKEN"},
			"dataplane": "ds-dp",
		}
	})

	_, key := createKey(t, adminURL, "ds-team", []string{"*"})

	// Settle only debits the day counter once the team HAS a day limit, so
	// the spending request must wait for the asynchronous first heartbeat to
	// apply the policy — observable as team_budget_day appearing in /v1/usage.
	deadline := time.Now().Add(5 * time.Second)
	for {
		usage := getUsage(t, dataURL, key)
		if _, ok := budgetLimitMicros(usage, "team_budget_day"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("day-only policy never applied (no team_budget_day in /v1/usage); last usage: %+v", usage)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// One successful request so real cost settles into the day counter.
	r := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("spending request: status %d, want 200", r.StatusCode)
	}

	// The consumption report rides the next heartbeat — poll. A window-blind
	// SpentOf (G4) reads the team's nil MONTH usage and reports 0 forever,
	// so requiring SpentMicroUSD > 0 is the whole kill.
	deadline = time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		seen := append([]policy.ConsumptionReport(nil), reports...)
		mu.Unlock()
		for _, rep := range seen {
			if rep.Rule != "daily-only" || rep.SpentMicroUSD <= 0 {
				continue
			}
			if rep.Period != v1alpha1.PeriodCalendarDay {
				t.Fatalf("daily-only spend reported against period %q, want %q: %+v", rep.Period, v1alpha1.PeriodCalendarDay, rep)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no consumption report for rule \"daily-only\" ever carried SpentMicroUSD > 0 — a window-blind SpentOf (G4) reports 0 forever; reports observed: %+v", seen)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A day-ONLY policy rule carrying the contact hint: there is no month rule,
// so tl.AdminContact is empty and only the day→month fallback in gateway.go
// can put the contact into the 402. The rule is soft, so the blocking mode
// comes from the config base (block wins on tie) — that keeps the 402 itself
// alive under G5, leaving the contact string as the only thing the mutation
// removes.
const dayContactPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: day-contact }
spec:
  subject: { team: contact-team }
  rules:
  - name: daily-with-contact
    failurePolicy: FailOpen
    budget:
      period: CalendarDay
      limitMilliUSD: 40000
      adminContact: "budget-team@example.com"
`

// TestE2EDayOnlyPolicyAdminContact kills G5: a day-only policy's
// adminContact must reach the 402 body. With the fallback deleted the 402
// still says "daily budget exceeded", so only an assertion on the contact
// itself can catch the regression.
func TestE2EDayOnlyPolicyAdminContact(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		// Config: roomy DAILY budget that BLOCKS. The policy's tighter soft
		// day rule inherits the block (block wins on tie, per window), so
		// the second request 402s regardless of the contact wiring.
		cfg["teams"] = map[string]any{
			"contact-team": map[string]any{
				"allowed_models": []any{"*"},
				"budget":         map[string]any{"usd_per_day": 1000.0, "on_exceeded": "block"},
			},
		}
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(polDir, "day.yaml"), []byte(dayContactPolicyYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})

	_, key := createKey(t, adminURL, "contact-team", []string{"*"})

	// First request reserves within the $40 day cap ($37 bound) and settles
	// $15 — the next request's bound no longer fits (reserve/settle economics,
	// see govConfig in e2e_test.go).
	r1 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first request: status %d, want 200", r1.StatusCode)
	}

	// Settlement is not synchronous with the client response, so poll until
	// the daily cap bites rather than trusting the very next request.
	deadline := time.Now().Add(5 * time.Second)
	var body []byte
	for {
		r2 := postMessages(t, dataURL, key, "claude-test")
		body, _ = io.ReadAll(r2.Body)
		r2.Body.Close()
		if r2.StatusCode == http.StatusPaymentRequired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daily budget never blocked: last status %d, body: %s", r2.StatusCode, body)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !strings.Contains(string(body), "daily budget exceeded") {
		t.Fatalf("402 must name the DAILY window; body: %s", body)
	}
	if !strings.Contains(string(body), "budget-team@example.com") {
		t.Fatalf("402 lost the day rule's adminContact — deleting the day→month fallback (G5) drops it silently while the message still reads as a plain budget denial; body: %s", body)
	}
}
