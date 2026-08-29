package budget

import (
	"testing"
	"time"
)

// TestKeyFormat pins the exact store-key shape governance builds. The last
// case is the reason the window tag leads: a team name may legally contain a
// colon, so an id must not be able to look like another window's namespace.
func TestKeyFormat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope string
		id    string
		w     Window
		want  string
	}{
		{"team month", ScopeTeam, "acme", CalendarMonthIn(nil), "budget:month:team:acme"},
		{"team day", ScopeTeam, "acme", Window{Kind: CalDay}, "budget:day:team:acme"},
		{"key month", ScopeKey, "ik_abc", CalendarMonthIn(nil), "budget:month:key:ik_abc"},
		{"user day", ScopeUser, "acme/sub-123", Window{Kind: CalDay}, "budget:day:user:acme/sub-123"},
		{"rolling carries its duration", ScopeTeam, "acme", Window{Kind: Rolling, Dur: 30 * 24 * time.Hour}, "budget:r720h0m0s:team:acme"},
		{"a colon in the id cannot impersonate a window", ScopeTeam, "day:team:victim", CalendarMonthIn(nil), "budget:month:team:day:team:victim"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Key(tc.scope, tc.id, tc.w); got != tc.want {
				t.Fatalf("Key = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDayAndMonthKeysDoNotShareBucket is the whole reason Key exists. Memory
// keys on the string alone and reads the Window only when it creates or rolls
// a bucket, so if a daily and a monthly counter for the same team shared a key
// they would share a bucket: the daily debit would land in the monthly total
// and the monthly cap would reset at midnight.
func TestDayAndMonthKeysDoNotShareBucket(t *testing.T) {
	b := NewMemory()
	b.now = func() time.Time { return time.Date(2026, 8, 25, 14, 59, 0, 0, time.UTC) }

	day := Window{Kind: CalDay}
	dayKey := Key(ScopeTeam, "acme", day)
	monthKey := Key(ScopeTeam, "acme", CalendarMonthIn(nil))
	if dayKey == monthKey {
		t.Fatalf("day and month keys for one team must differ, both are %q", dayKey)
	}

	b.Debit(dayKey, 10_000_000, day)

	if got := b.Spent(monthKey, CalendarMonthIn(nil)); got != 0 {
		t.Fatalf("month bucket spent = %d after a day-only debit, want 0", got)
	}
	if got := b.Spent(dayKey, day); got != 10_000_000 {
		t.Fatalf("day bucket spent = %d, want 10000000", got)
	}
	if got, want := b.ResetsAt(monthKey, CalendarMonthIn(nil)), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("month bucket ResetsAt = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if got, want := b.ResetsAt(dayKey, day), time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("day bucket ResetsAt = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
