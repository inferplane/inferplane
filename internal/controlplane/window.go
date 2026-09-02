package controlplane

import (
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// windowIDFor computes the control-plane-owned budget window epoch for a
// rule's period at the given instant (roadmap ② window epochs): "2026-09"
// for CalendarMonth, "2026-09-02" for CalendarDay, both UTC. The control
// plane owning the id is the point — rollover becomes a deliberate epoch
// change every data plane observes in its next grant, not a heuristic each
// side infers separately (and differently, under timezone skew) from a
// decreasing counter. Operator-legible calendar ids beat opaque counters:
// they appear in the debug view and in ledger rows, and an operator can
// read "2026-09" without a decoder. UTC deliberately — the control plane's
// window must be ONE window for the whole fleet, so a per-plane
// budget_timezone cannot apply here (mayu bridges the phase difference by
// baselining its local counter at each epoch change).
func windowIDFor(period v1alpha1.BudgetPeriod, now time.Time) string {
	u := now.UTC()
	if period == v1alpha1.PeriodCalendarDay {
		return u.Format("2006-01-02")
	}
	// Empty reads as CalendarMonth — the meaning every rule had before
	// BudgetRule.period existed, same rule as everywhere on this wire.
	return u.Format("2006-01")
}
