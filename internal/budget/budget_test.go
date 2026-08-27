package budget

import (
	"testing"
	"time"
)

// rolling30d is what this test expressed as a bare 30*24*time.Hour before the
// window became a value type.
var rolling30d = Window{Kind: Rolling, Dur: 30 * 24 * time.Hour}

func TestBudgetTwoPhaseMicros(t *testing.T) {
	b := NewMemory()
	now := time.Unix(1_700_000_000, 0)
	b.now = func() time.Time { return now }
	key := "team:month"
	limit := int64(5_000_000) // 5 USD in µUSD
	if d := b.Check(key, 4_000_000, limit, rolling30d); d != Allow {
		t.Fatalf("under: %v", d)
	}
	b.Debit(key, 4_000_000, rolling30d)
	if d := b.Check(key, 2_000_000, limit, rolling30d); d != Block {
		t.Fatalf("4M+2M>5M should block: %v", d)
	}
	if d := b.Check(key, 500_000, limit, rolling30d); d != Allow {
		t.Fatalf("4M+0.5M≤5M should allow: %v", d)
	}
}

// TestCalendarMonthStillAnchorsToNextUTCMonth is the refactor pin for the
// time.Duration→Window change: CalendarMonth must resolve to exactly the
// instant the old negative-duration sentinel did — the first instant of next
// month in UTC — including across a year boundary and for a t whose own
// Location is not UTC. Every want below was produced by running BOTH
// implementations side by side; they agreed on all of them.
func TestCalendarMonthStillAnchorsToNextUTCMonth(t *testing.T) {
	kst := time.FixedZone("KST", 9*60*60)
	for _, tc := range []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "mid-month",
			now:  time.Date(2026, 8, 25, 14, 59, 0, 0, time.UTC),
			want: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "december rolls the year",
			now:  time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
			want: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "february end",
			now:  time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC),
			want: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "a non-UTC instant still anchors in UTC",
			now:  time.Date(2026, 8, 31, 20, 0, 0, 0, kst),
			want: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewMemory()
			b.now = func() time.Time { return tc.now }
			got := b.ResetsAt(Key(ScopeTeam, "acme", CalendarMonth), CalendarMonth)
			if !got.Equal(tc.want) {
				t.Fatalf("ResetsAt = %s, want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

// TestWindowKindBoundaries covers the two branches windowEnd gained. CalDay is
// now consumed by the governance daily budget window; this test remains the
// boundary-arithmetic pin.
func TestWindowKindBoundaries(t *testing.T) {
	kst := time.FixedZone("KST", 9*60*60)
	for _, tc := range []struct {
		name string
		now  time.Time
		w    Window
		want time.Time
	}{
		{
			name: "CalDay rolls at operator midnight",
			now:  time.Date(2026, 8, 25, 14, 59, 0, 0, time.UTC), // 08-25 23:59 KST
			w:    Window{Kind: CalDay, Loc: kst},
			want: time.Date(2026, 8, 25, 15, 0, 0, 0, time.UTC), // 08-26 00:00 KST
		},
		{
			name: "CalDay with a nil Loc rolls at UTC midnight",
			now:  time.Date(2026, 8, 25, 14, 59, 0, 0, time.UTC),
			w:    Window{Kind: CalDay},
			want: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "CalMonth honours a non-nil Loc",
			now:  time.Date(2026, 8, 31, 20, 0, 0, 0, time.UTC), // 09-01 05:00 KST
			w:    Window{Kind: CalMonth, Loc: kst},
			want: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC), // 10-01 00:00 KST
		},
		{
			name: "Rolling is t plus Dur",
			now:  time.Date(2026, 8, 25, 14, 59, 0, 0, time.UTC),
			w:    Window{Kind: Rolling, Dur: 90 * time.Minute},
			want: time.Date(2026, 8, 25, 16, 29, 0, 0, time.UTC),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewMemory()
			b.now = func() time.Time { return tc.now }
			if got := b.ResetsAt("k", tc.w); !got.Equal(tc.want) {
				t.Fatalf("ResetsAt = %s, want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}
