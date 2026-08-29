package tier

import (
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/policy"
)

func TestTableGetEmpty(t *testing.T) {
	tb := NewTable()
	if m := tb.Get("ml-platform"); m != nil {
		t.Fatalf("empty table returned %v", m)
	}
}

func TestTableSetAndGet(t *testing.T) {
	tb := NewTable()
	tb.Set([]policy.ActiveTier{
		{Policy: "p", Rule: "r", Team: "ml-platform", ThresholdPercent: 80, Substitute: map[string]string{"claude-haiku-4-5": "glm-4.7-gpu"}},
	})
	got := tb.Get("ml-platform")
	if got["claude-haiku-4-5"] != "glm-4.7-gpu" {
		t.Fatalf("got %v", got)
	}
	if tb.Get("other-team") != nil {
		t.Fatal("unrelated team got a substitution map")
	}
}

// A defensive copy: mutating the returned map must not affect the table.
func TestTableGetIsACopy(t *testing.T) {
	tb := NewTable()
	tb.Set([]policy.ActiveTier{{Team: "t", ThresholdPercent: 80, Substitute: map[string]string{"a": "b"}}})
	got := tb.Get("t")
	got["a"] = "tampered"
	if tb.Get("t")["a"] != "b" {
		t.Fatal("Get result was not a defensive copy")
	}
}

// Two rules matching the same team disagree on one key: the higher
// threshold (deeper budget pressure) wins.
func TestTableSetConflictHigherThresholdWins(t *testing.T) {
	tb := NewTable()
	tb.Set([]policy.ActiveTier{
		{Policy: "p1", Rule: "r1", Team: "t", ThresholdPercent: 80, Substitute: map[string]string{"a": "low-pressure-target"}},
		{Policy: "p2", Rule: "r2", Team: "t", ThresholdPercent: 95, Substitute: map[string]string{"a": "high-pressure-target"}},
	})
	if got := tb.Get("t")["a"]; got != "high-pressure-target" {
		t.Fatalf("got %q, want high-pressure-target", got)
	}
}

// Equal thresholds break the tie lexicographically by (policy, rule), and
// the result must not depend on slice order.
func TestTableSetConflictTieBreaksByPolicyRule(t *testing.T) {
	forward := []policy.ActiveTier{
		{Policy: "a-policy", Rule: "r", Team: "t", ThresholdPercent: 80, Substitute: map[string]string{"x": "from-a"}},
		{Policy: "z-policy", Rule: "r", Team: "t", ThresholdPercent: 80, Substitute: map[string]string{"x": "from-z"}},
	}
	reversed := []policy.ActiveTier{forward[1], forward[0]}

	tb1 := NewTable()
	tb1.Set(forward)
	tb2 := NewTable()
	tb2.Set(reversed)

	if got := tb1.Get("t")["x"]; got != "from-a" {
		t.Fatalf("forward order: got %q, want from-a", got)
	}
	if got := tb2.Get("t")["x"]; got != "from-a" {
		t.Fatalf("reversed order: got %q, want from-a (order must not matter)", got)
	}
}

// Non-conflicting keys from different active tiers union cleanly.
func TestTableSetUnionsNonConflictingKeys(t *testing.T) {
	tb := NewTable()
	tb.Set([]policy.ActiveTier{
		{Policy: "p1", Rule: "r1", Team: "t", ThresholdPercent: 80, Substitute: map[string]string{"a": "x"}},
		{Policy: "p2", Rule: "r2", Team: "t", ThresholdPercent: 90, Substitute: map[string]string{"b": "y"}},
	})
	got := tb.Get("t")
	if got["a"] != "x" || got["b"] != "y" || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestLatchMonotoneWithinWindow(t *testing.T) {
	l := NewLatch()
	thresholds := []int{80, 95}
	window := "2026-08"

	if idx := l.Evaluate("p/r", window, thresholds, 50); idx != -1 {
		t.Fatalf("50%% utilization: idx=%d, want -1", idx)
	}
	if idx := l.Evaluate("p/r", window, thresholds, 85); idx != 0 {
		t.Fatalf("85%%: idx=%d, want 0", idx)
	}
	// A later, LOWER sample must not deactivate the tier within the window.
	if idx := l.Evaluate("p/r", window, thresholds, 60); idx != 0 {
		t.Fatalf("dip to 60%% within window: idx=%d, want latched 0", idx)
	}
	// Crossing the next threshold escalates.
	if idx := l.Evaluate("p/r", window, thresholds, 96); idx != 1 {
		t.Fatalf("96%%: idx=%d, want 1", idx)
	}
	// A dip after escalation stays at the higher tier.
	if idx := l.Evaluate("p/r", window, thresholds, 10); idx != 1 {
		t.Fatalf("dip after escalation: idx=%d, want latched 1", idx)
	}
}

func TestLatchResetsOnWindowChange(t *testing.T) {
	l := NewLatch()
	thresholds := []int{80}
	l.Evaluate("p/r", "2026-08", thresholds, 90)
	if idx := l.Evaluate("p/r", "2026-08", thresholds, 90); idx != 0 {
		t.Fatalf("still in window: idx=%d, want 0", idx)
	}
	if idx := l.Evaluate("p/r", "2026-09", thresholds, 10); idx != -1 {
		t.Fatalf("new window with low utilization: idx=%d, want -1 (reset)", idx)
	}
}

// Different rule keys never interfere with each other's latch state.
func TestLatchKeysAreIndependent(t *testing.T) {
	l := NewLatch()
	l.Evaluate("p/r1", "2026-08", []int{80}, 90)
	if idx := l.Evaluate("p/r2", "2026-08", []int{80}, 10); idx != -1 {
		t.Fatalf("unrelated rule key latched: idx=%d", idx)
	}
}

func TestLatchForget(t *testing.T) {
	l := NewLatch()
	l.Evaluate("p/r", "2026-08", []int{80}, 90)
	l.Forget("p/r")
	if idx := l.Evaluate("p/r", "2026-08", []int{80}, 10); idx != -1 {
		t.Fatalf("forgotten key still latched: idx=%d", idx)
	}
}

func TestWindowKeyIsCalendarMonthUTC(t *testing.T) {
	got := WindowKey(time.Date(2026, time.August, 31, 23, 59, 0, 0, time.UTC))
	if got != "2026-08" {
		t.Fatalf("WindowKey = %q, want 2026-08", got)
	}
	// A different timezone must still resolve against the UTC calendar.
	loc := time.FixedZone("UTC-5", -5*3600)
	got = WindowKey(time.Date(2026, time.September, 1, 3, 0, 0, 0, loc)) // 2026-09-01 08:00 UTC
	if got != "2026-09" {
		t.Fatalf("WindowKey across tz = %q, want 2026-09", got)
	}
}

// Local review of PR #65 (CONFIRMED): a latch survives a policy reload by
// design (ADR-041 D2), so a rule edited to FEWER tiers could return a latched
// tierIndex out of range for the new thresholds slice — both consumers index
// it unchecked (controlplane handleSync, mayu gateway), a per-heartbeat /
// per-request panic. The latched index clamps to the deepest tier that still
// exists; monotonicity within the window is preserved.
func TestEvaluateClampsLatchedIndexAfterTiersShrink(t *testing.T) {
	l := NewLatch()
	if got := l.Evaluate("p/r", "2026-08", []int{50, 80, 95}, 96); got != 2 {
		t.Fatalf("latch at deepest tier: got %d", got)
	}
	// Rule edited to a single tier; the latch state survives the reload.
	if got := l.Evaluate("p/r", "2026-08", []int{50}, 10); got != 0 {
		t.Fatalf("shrunk thresholds must clamp the latched index into range, got %d", got)
	}
	// An emptied thresholds list means no tier can be active at all.
	if got := l.Evaluate("p/r", "2026-08", nil, 10); got != -1 {
		t.Fatalf("empty thresholds must yield -1, got %d", got)
	}
}
