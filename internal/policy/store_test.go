package policy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

func writePolicy(t *testing.T, dir, name, body string) string {
	t.Helper()
	f := filepath.Join(dir, name)
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

const storeYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-caps }
spec:
  subject: { team: platform-eng }
  rules:
  - name: cap-soft
    failurePolicy: FailOpen
    budget: { limitMilliUSD: 9000000 }
  - name: rate
    failurePolicy: FailOpen
    rate: { rpm: 300, tpm: 2000000 }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: team-caps-strict }
spec:
  subject: { team: platform-eng }
  rules:
  - name: cap-hard
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 5000000, hardCap: true }
  - name: rate-tighter
    failurePolicy: FailOpen
    rate: { rpm: 100 }
  - name: models
    failurePolicy: FailOpen
    modelAccess: { allow: ["claude-sonnet-4-6", "sonnet-alias"] }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: user-models }
spec:
  subject: { user: junseok }
  rules:
  - name: haiku-only
    failurePolicy: FailOpen
    modelAccess: { allow: ["claude-haiku-4-5"] }
`

// Two team policies merge most-restrictive: smallest non-zero limit binds
// each dimension, and the binding budget's hardCap decides block vs warn.
func TestTeamLimitsMerge(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", storeYAML)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tl, ok := s.TeamLimits("platform-eng")
	if !ok {
		t.Fatal("platform-eng not found")
	}
	if tl.BudgetMicrosPerMonth != 5_000_000_000 || !tl.BudgetHard {
		t.Fatalf("budget merge wrong: %+v", tl)
	}
	if tl.RPM != 100 || tl.TPM != 2_000_000 {
		t.Fatalf("rate merge wrong: %+v", tl)
	}
	if _, ok := s.TeamLimits("other-team"); ok {
		t.Fatal("unmatched team reported limits")
	}
}

// An explicit unlimited: true rule must never narrow OR widen a binding
// limit set by another rule for the same team — regardless of which rule
// is processed first. Before the fix, an Unlimited budget rule (internally
// LimitMicroUSD: 0) processed AFTER a real one satisfied the merge's
// "0 or smaller wins" comparison and silently erased the real cap.
func TestTeamLimitsMergeUnlimitedNeverErasesARealLimit(t *testing.T) {
	real := &Policy{Name: "real", Subject: Subject{Team: "t"},
		Rules: []Rule{{Name: "cap", Budget: &Budget{LimitMicroUSD: 5_000_000, HardCap: true}}}}
	unlimited := &Policy{Name: "declared-unlimited", Subject: Subject{Team: "t"},
		Rules: []Rule{{Name: "no-cap", Budget: &Budget{Unlimited: true}}}}

	for _, order := range [][]*Policy{{real, unlimited}, {unlimited, real}} {
		got := mergeTeamLimits(order)
		tl, ok := got["t"]
		if !ok {
			t.Fatalf("order %v: team missing from merge result", order)
		}
		if tl.BudgetMicrosPerMonth != 5_000_000 || !tl.BudgetHard {
			t.Fatalf("order %v: unlimited rule corrupted the real budget: %+v", order, tl)
		}
	}
}

// A team with ONLY an explicit unlimited declaration (no other budget/rate
// rule) still gets a merge entry — the declaration itself is the policy
// decision, and it must be able to shadow a config/DB default the same way
// a real numeric rule would, rather than falling through to it.
func TestTeamLimitsMergeUnlimitedOnlyStillContributes(t *testing.T) {
	p := &Policy{Name: "declared-unlimited", Subject: Subject{Team: "t"},
		Rules: []Rule{{Name: "no-cap", Budget: &Budget{Unlimited: true}}}}
	got := mergeTeamLimits([]*Policy{p})
	tl, ok := got["t"]
	if !ok {
		t.Fatal("an explicit unlimited declaration must still contribute a merge entry")
	}
	if tl.BudgetMicrosPerMonth != 0 || tl.BudgetHard {
		t.Fatalf("unlimited-only merge should be all-zero: %+v", tl)
	}
}

// A day rule and a month rule in one policy land in one TeamLimits, each in
// its own window: the day limit, hard flag, and contact never bleed into the
// month fields, nor the reverse. The cross-window hard-flag assertions are
// what stop the two flags being wired to one variable — a bug that would
// make a soft daily cap block, or a hard monthly cap only warn.
func TestTeamLimitsMergeDayAndMonthFoldIndependently(t *testing.T) {
	p := &Policy{Name: "two-windows", Subject: Subject{Team: "t"},
		Rules: []Rule{
			{Name: "day-cap", Budget: &Budget{LimitMicroUSD: 50_000_000, HardCap: true, AdminContact: "day@ops", Period: v1alpha1.PeriodCalendarDay}},
			{Name: "month-cap", Budget: &Budget{LimitMicroUSD: 1_000_000_000, AdminContact: "month@ops"}},
		}}
	got := mergeTeamLimits([]*Policy{p})
	tl, ok := got["t"]
	if !ok {
		t.Fatal("team missing from merge result")
	}
	if tl.BudgetMicrosPerDay != 50_000_000 || tl.BudgetMicrosPerMonth != 1_000_000_000 {
		t.Fatalf("day and month limits did not land independently: %+v", tl)
	}
	if !tl.BudgetDayHard || tl.BudgetHard {
		t.Fatalf("hard day rule must set BudgetDayHard and leave BudgetHard false: %+v", tl)
	}
	if tl.AdminContactDay != "day@ops" || tl.AdminContact != "month@ops" {
		t.Fatalf("adminContact landed in the wrong window: %+v", tl)
	}
}

// The mirror of the above: a hard MONTH rule sets BudgetHard and must not
// set BudgetDayHard, and its contact stays in the month field.
func TestTeamLimitsMergeHardMonthDoesNotHardenDay(t *testing.T) {
	p := &Policy{Name: "two-windows", Subject: Subject{Team: "t"},
		Rules: []Rule{
			{Name: "day-cap", Budget: &Budget{LimitMicroUSD: 50_000_000, AdminContact: "day@ops", Period: v1alpha1.PeriodCalendarDay}},
			{Name: "month-cap", Budget: &Budget{LimitMicroUSD: 1_000_000_000, HardCap: true, AdminContact: "month@ops"}},
		}}
	tl, ok := mergeTeamLimits([]*Policy{p})["t"]
	if !ok {
		t.Fatal("team missing from merge result")
	}
	if !tl.BudgetHard || tl.BudgetDayHard {
		t.Fatalf("hard month rule must set BudgetHard and leave BudgetDayHard false: %+v", tl)
	}
	if tl.AdminContact != "month@ops" || tl.AdminContactDay != "day@ops" {
		t.Fatalf("adminContact landed in the wrong window: %+v", tl)
	}
}

// Most-restrictive-wins is computed PER WINDOW: two day rules fold to the
// smaller day limit without touching the month limit, and two month rules
// the mirror.
func TestTeamLimitsMergeMostRestrictivePerWindow(t *testing.T) {
	twoDay := &Policy{Name: "two-day", Subject: Subject{Team: "t"},
		Rules: []Rule{
			{Name: "day-loose", Budget: &Budget{LimitMicroUSD: 80_000_000, Period: v1alpha1.PeriodCalendarDay}},
			{Name: "day-tight", Budget: &Budget{LimitMicroUSD: 30_000_000, Period: v1alpha1.PeriodCalendarDay}},
			{Name: "month-cap", Budget: &Budget{LimitMicroUSD: 1_000_000_000}},
		}}
	tl, ok := mergeTeamLimits([]*Policy{twoDay})["t"]
	if !ok {
		t.Fatal("team missing from merge result")
	}
	if tl.BudgetMicrosPerDay != 30_000_000 {
		t.Fatalf("smaller day limit must bind: %+v", tl)
	}
	if tl.BudgetMicrosPerMonth != 1_000_000_000 {
		t.Fatalf("month limit must be untouched by the day fold: %+v", tl)
	}

	twoMonth := &Policy{Name: "two-month", Subject: Subject{Team: "t"},
		Rules: []Rule{
			{Name: "month-loose", Budget: &Budget{LimitMicroUSD: 2_000_000_000}},
			{Name: "month-tight", Budget: &Budget{LimitMicroUSD: 900_000_000}},
			{Name: "day-cap", Budget: &Budget{LimitMicroUSD: 50_000_000, Period: v1alpha1.PeriodCalendarDay}},
		}}
	tl, ok = mergeTeamLimits([]*Policy{twoMonth})["t"]
	if !ok {
		t.Fatal("team missing from merge result")
	}
	if tl.BudgetMicrosPerMonth != 900_000_000 {
		t.Fatalf("smaller month limit must bind: %+v", tl)
	}
	if tl.BudgetMicrosPerDay != 50_000_000 {
		t.Fatalf("day limit must be untouched by the month fold: %+v", tl)
	}
}

// The per-window twin of TestTeamLimitsMergeUnlimitedNeverErasesARealLimit:
// an explicit unlimited rule is window-agnostic and must not erase, narrow
// or widen a real DAY limit nor a real MONTH limit — regardless of which
// rule is processed first.
func TestTeamLimitsMergeUnlimitedTouchesNeitherWindow(t *testing.T) {
	real := &Policy{Name: "real", Subject: Subject{Team: "t"},
		Rules: []Rule{
			{Name: "day-cap", Budget: &Budget{LimitMicroUSD: 50_000_000, HardCap: true, Period: v1alpha1.PeriodCalendarDay}},
			{Name: "month-cap", Budget: &Budget{LimitMicroUSD: 1_000_000_000, HardCap: true}},
		}}
	unlimited := &Policy{Name: "declared-unlimited", Subject: Subject{Team: "t"},
		Rules: []Rule{{Name: "no-cap", Budget: &Budget{Unlimited: true}}}}

	for _, order := range [][]*Policy{{real, unlimited}, {unlimited, real}} {
		got := mergeTeamLimits(order)
		tl, ok := got["t"]
		if !ok {
			t.Fatalf("order %v: team missing from merge result", order)
		}
		if tl.BudgetMicrosPerDay != 50_000_000 || !tl.BudgetDayHard {
			t.Fatalf("order %v: unlimited rule corrupted the real day budget: %+v", order, tl)
		}
		if tl.BudgetMicrosPerMonth != 1_000_000_000 || !tl.BudgetHard {
			t.Fatalf("order %v: unlimited rule corrupted the real month budget: %+v", order, tl)
		}
	}
}

// A MONTH-only policy must leave the day window untouched (zero limit, soft)
// — the fall-through the gateway relies on to keep an operator's
// config/keystore daily cap binding when a policy document says nothing
// about the day.
func TestTeamLimitsMergeMonthOnlyLeavesDayWindowZero(t *testing.T) {
	p := &Policy{Name: "month-only", Subject: Subject{Team: "t"},
		Rules: []Rule{{Name: "month-cap", Budget: &Budget{LimitMicroUSD: 1_000_000_000, HardCap: true}}}}
	tl, ok := mergeTeamLimits([]*Policy{p})["t"]
	if !ok {
		t.Fatal("team missing from merge result")
	}
	if tl.BudgetMicrosPerDay != 0 || tl.BudgetDayHard {
		t.Fatalf("month-only policy must not manufacture a day limit: %+v", tl)
	}
	if tl.BudgetMicrosPerMonth != 1_000_000_000 || !tl.BudgetHard {
		t.Fatalf("month limit lost: %+v", tl)
	}
}

func TestModelAllowed(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", storeYAML)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	canon := func(m string) string {
		if m == "sonnet-alias" {
			return "claude-sonnet-4-6"
		}
		return m
	}

	// Team rule allows sonnet (direct and via canonicalized alias entry).
	if !s.ModelAllowed("platform-eng", "", "claude-sonnet-4-6", canon) {
		t.Fatal("team-allowed model denied")
	}
	if s.ModelAllowed("platform-eng", "", "claude-opus-4-8", canon) {
		t.Fatal("team-restricted model allowed")
	}
	// User rule ANDs on top of the team rule: junseok in platform-eng may
	// only use models BOTH lists allow — most-restrictive-wins means the
	// disjoint lists deny everything for this pairing.
	if s.ModelAllowed("platform-eng", "junseok", "claude-sonnet-4-6", canon) {
		t.Fatal("user restriction not applied on top of team's")
	}
	if s.ModelAllowed("platform-eng", "junseok", "claude-haiku-4-5", canon) {
		t.Fatal("team restriction not applied on top of user's")
	}
	// The same user under no team restriction keeps their haiku access.
	if !s.ModelAllowed("other-team", "junseok", "claude-haiku-4-5", canon) {
		t.Fatal("user-allowed model denied")
	}
	// No matching policy at all → no restriction.
	if !s.ModelAllowed("other-team", "someone", "anything", canon) {
		t.Fatal("unmatched subject restricted")
	}
}

// Rules this build cannot enforce are rejected at load — a data plane must
// never hold a policy it silently isn't enforcing.
func TestUnenforceableRejected(t *testing.T) {
	cases := []struct{ name, body string }{
		{"routing rule", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: r }
spec:
  subject: { team: t }
  rules:
  - name: pin
    failurePolicy: FailOpen
    routing: { onAffinityConflict: PreferAffinity }
`},
		{"user-subject budget", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: u }
spec:
  subject: { user: junseok }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1000, hardCap: true }
`},
		{"team-and-user budget (would merge to nothing)", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: tu }
spec:
  subject: { team: demo, user: junseok }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1000, hardCap: true }
`},
		{"team-and-user rate", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: tur }
spec:
  subject: { team: demo, user: junseok }
  rules:
  - name: r
    failurePolicy: FailOpen
    rate: { rpm: 10 }
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writePolicy(t, dir, "p.yaml", tc.body)
			_, err := NewStore(dir)
			var ue *UnsupportedError
			if !errors.As(err, &ue) {
				t.Fatalf("want *UnsupportedError, got %v", err)
			}
		})
	}
}

// A failed reload keeps the previous snapshot serving (never-fatal posture).
func TestReloadKeepsOldOnError(t *testing.T) {
	dir := t.TempDir()
	f := writePolicy(t, dir, "p.yaml", storeYAML)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := os.WriteFile(f, []byte("apiVersion: nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err == nil {
		t.Fatal("bad edit reloaded without error")
	}
	if _, ok := s.TeamLimits("platform-eng"); !ok {
		t.Fatal("previous snapshot lost after failed reload")
	}

	// A good edit swaps the set.
	good := strings.Replace(storeYAML, "rpm: 100", "rpm: 50", 1)
	if err := os.WriteFile(f, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if tl, _ := s.TeamLimits("platform-eng"); tl.RPM != 50 {
		t.Fatalf("reload didn't apply: %+v", tl)
	}
}

// changed() must notice edits, new files, and deletions — it is what the
// watcher polls between reloads.
func TestChanged(t *testing.T) {
	dir := t.TempDir()
	f := writePolicy(t, dir, "p.yaml", storeYAML)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s.changed() {
		t.Fatal("changed() true right after load")
	}
	// mtime granularity can be coarse; force a distinct mtime.
	if err := os.Chtimes(f, time.Now(), time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !s.changed() {
		t.Fatal("mtime bump not detected")
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	writePolicy(t, dir, "extra.json", `{"apiVersion":"inferplane.dev/v1alpha1","kind":"GovernancePolicy","metadata":{"name":"x"},"spec":{"subject":{"team":"x"},"rules":[{"name":"r","failurePolicy":"FailOpen","rate":{"rpm":1}}]}}`)
	if !s.changed() {
		t.Fatal("new file not detected")
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if !s.changed() {
		t.Fatal("deleted file not detected")
	}
}

// Regression (review finding, critical): a modelAccess-only team policy must
// NOT create a TeamLimits entry — an all-zero (= unlimited) entry would let
// the gateway's lookup chain shadow the team's DB-record/config budget.
func TestModelAccessOnlyContributesNoTeamLimits(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "p.yaml", `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: m }
spec:
  subject: { team: demo }
  rules:
  - name: models
    failurePolicy: FailOpen
    modelAccess: { allow: ["claude-haiku-4-5"] }
`)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if tl, ok := s.TeamLimits("demo"); ok {
		t.Fatalf("modelAccess-only policy manufactured TeamLimits %+v — would shadow base limits with unlimited", tl)
	}
	// The modelAccess rule itself still enforces.
	if s.ModelAllowed("demo", "", "claude-opus-4-8", nil) {
		t.Fatal("modelAccess rule not enforced")
	}
}

// PR #50 review finding: a stray Reload/Watch on a control-plane-fed store
// must fail loudly instead of silently wiping the distributed set with an
// empty file scan.
func TestEmptyStoreRejectsReloadAndWatch(t *testing.T) {
	s := NewEmptyStore()
	rejected := s.ApplyWire(nil)
	if len(rejected) != 0 {
		t.Fatalf("empty ApplyWire rejected: %v", rejected)
	}
	if err := s.Reload(); err == nil {
		t.Fatal("Reload on a control-plane-fed store must error")
	}
	called := false
	s.Watch(context.Background(), func(error) { called = true })
	if !called {
		t.Fatal("Watch on a control-plane-fed store must report and return")
	}
}
