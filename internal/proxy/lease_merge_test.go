package proxy

import (
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
)

func TestLeaseMergeKeepsHardAllowanceAboveSoftThreshold(t *testing.T) {
	for _, period := range []v1alpha1.BudgetPeriod{v1alpha1.PeriodCalendarMonth, v1alpha1.PeriodCalendarDay} {
		for _, reverse := range []bool{false, true} {
			t.Run(string(period)+map[bool]string{false: "/forward", true: "/reverse"}[reverse], func(t *testing.T) {
				now := time.Now()
				grants := []policy.LeaseGrant{
					{Team: "t", Period: period, AllowanceMicroUSD: 1_000, ExpiresAt: now.Add(-time.Second)},
					{Team: "t", Period: period, AllowanceMicroUSD: 6_000, HardCap: true, ExpiresAt: now.Add(time.Minute)},
					{Team: "t", Period: period, AllowanceMicroUSD: 3_000, HardCap: true, ExpiresAt: now.Add(2 * time.Minute)},
				}
				if reverse {
					grants[0], grants[2] = grants[2], grants[0]
				}
				table := NewLeaseTable()
				table.set(grants)
				lease, ok := table.Get("t", period)
				if !ok || lease.AllowanceMicroUSD != 3_000 || !lease.HardCap || !lease.ExpiresAt.Equal(now.Add(time.Minute)) {
					t.Fatalf("soft lease narrowed or expired hard authority: %+v, found=%v", lease, ok)
				}
				if blocked, reason := table.Blocked("t"); blocked {
					t.Fatalf("expired soft lease blocked valid hard allowance: %s", reason)
				}
			})
		}
	}
}
