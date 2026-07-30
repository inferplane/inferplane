package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
