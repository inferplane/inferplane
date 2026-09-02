package controlplane

// Window-epoch tests (roadmap ② second half): the control plane owns the
// budget window — rollover is a deliberate, clock-driven epoch change that
// resets the ledger wholesale, stamps every grant, and refuses reports
// stamped with a previous epoch. No decrease-detection heuristics involved
// (those remain only as the fallback for epoch-less reports from older
// builds).

import (
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
)

func TestWindowIDFor(t *testing.T) {
	// 23:30 UTC-turned-local must not matter: the id is UTC by definition.
	at := time.Date(2027, 3, 31, 23, 30, 0, 0, time.UTC)
	if got := windowIDFor(v1alpha1.PeriodCalendarMonth, at); got != "2027-03" {
		t.Fatalf("month id = %q, want 2027-03", got)
	}
	if got := windowIDFor("", at); got != "2027-03" {
		t.Fatalf("empty period must read as CalendarMonth: %q", got)
	}
	if got := windowIDFor(v1alpha1.PeriodCalendarDay, at); got != "2027-03-31" {
		t.Fatalf("day id = %q, want 2027-03-31", got)
	}
}

// The full epoch lifecycle over one rollover: grants carry the epoch, a
// stale-epoch report is refused, and the fresh window starts with the full
// limit even though the old one was exhausted.
func TestWindowEpochRollsLedgerAndRefusesStaleReports(t *testing.T) {
	s, ts := newTestServer(t, "")
	clock := time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	resp := doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1"})
	if len(resp.Leases) != 1 || resp.Leases[0].WindowID != "2027-03" {
		t.Fatalf("first grant must carry the current epoch: %+v", resp.Leases)
	}

	// Exhaust the window: report the full $0.10 limit in-epoch.
	resp = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1", Reports: []policy.ConsumptionReport{
		{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 100_000, WindowID: "2027-03"},
	}})
	if got := resp.Leases[0].AllowanceMicroUSD; got != 100_000 {
		t.Fatalf("exhausted window: allowance = %d, want spent+0 = 100000", got)
	}

	// The month rolls. A lagging report still stamped with the OLD epoch —
	// carrying the old window's whole spend — must be refused, and the
	// fresh window must grant from a zeroed ledger.
	clock = time.Date(2027, 4, 1, 0, 0, 5, 0, time.UTC)
	resp = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1", Reports: []policy.ConsumptionReport{
		{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 100_000, WindowID: "2027-03"},
	}})
	if resp.Leases[0].WindowID != "2027-04" {
		t.Fatalf("post-rollover grant epoch = %q, want 2027-04", resp.Leases[0].WindowID)
	}
	if got := resp.Leases[0].AllowanceMicroUSD; got != 10_000 {
		t.Fatalf("fresh window allowance = %d, want a full 10000 grant (stale report booked, or ledger not reset)", got)
	}

	// An in-epoch report IS absorbed.
	resp = doSync(t, ts.URL, "", policy.SyncRequest{Dataplane: "dp1", Reports: []policy.ConsumptionReport{
		{Policy: "team-a", Rule: "cap", Team: "alpha", SpentMicroUSD: 3_000, WindowID: "2027-04"},
	}})
	if got := resp.Leases[0].AllowanceMicroUSD; got != 13_000 {
		t.Fatalf("in-epoch report: allowance = %d, want 3000+10000", got)
	}
}

// fakeLedgerStore hands SetLedgerStore a fixed row set.
type fakeLedgerStore struct{ rows []LedgerRow }

func (f *fakeLedgerStore) Load() ([]LedgerRow, []DataplaneRow, error) { return f.rows, nil, nil }
func (f *fakeLedgerStore) SaveDataplane(DataplaneRow, []LedgerRow) error {
	return nil
}
func (f *fakeLedgerStore) DeleteDataplane(string) error { return nil }
func (f *fakeLedgerStore) Close() error                 { return nil }

// A persisted row from a previous epoch must NOT restore (the rollover
// already forgot that spend); a current-epoch row and a pre-epoch (empty
// windowID) row must.
func TestLedgerLoadSkipsStaleEpochRows(t *testing.T) {
	s, _ := newTestServer(t, "")
	now := time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	// Period is normalized to CalendarMonth at conversion (never empty
	// internally), and write-behind rows carry that normalized value.
	month := v1alpha1.PeriodCalendarMonth
	err := s.SetLedgerStore(&fakeLedgerStore{rows: []LedgerRow{
		{Policy: "team-a", Rule: "cap", Dataplane: "dp-stale", Period: month, WindowID: "2027-02", Spent: 50_000},
		{Policy: "team-a", Rule: "cap", Dataplane: "dp-live", Period: month, WindowID: "2027-03", Spent: 20_000},
		{Policy: "team-a", Rule: "cap", Dataplane: "dp-old-build", Period: month, WindowID: "", Spent: 10_000},
	}})
	if err != nil {
		t.Fatalf("SetLedgerStore: %v", err)
	}
	l := s.ledger[ruleKey{policy: "team-a", rule: "cap"}]
	if _, restored := l.spent["dp-stale"]; restored {
		t.Fatalf("a previous epoch's row must not restore: %+v", l.spent)
	}
	if l.spent["dp-live"] != 20_000 || l.spent["dp-old-build"] != 10_000 {
		t.Fatalf("current-epoch and pre-epoch rows must restore: %+v", l.spent)
	}
	if l.windowID != "2027-03" {
		t.Fatalf("load must leave the ledger on the current epoch, got %q", l.windowID)
	}
}
