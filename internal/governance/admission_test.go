package governance

import (
	"sync"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/pricing"
)

func TestAdmissionSettlesCountersExactlyOnce(t *testing.T) {
	l := limiter.NewMemory()
	b := budget.NewMemory()
	g := NewGovernor(map[string]TeamPolicy{"t": {TokensPerMinute: 1000, TokensPerDay: 1000, BudgetMicrosPerMonth: 1000}}, l, b, nil)
	s := Subject{Team: "t"}
	d := g.PreCheck(s, KeyPolicy{}, 100)
	if !d.Allowed {
		t.Fatal(d)
	}
	defer d.Admission.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cost, missing := g.Settle(s, KeyPolicy{}, "p", "m", pricing.Usage{Input: 20}, testTable(), d.Admission)
			if cost != 20 || missing {
				t.Errorf("cost=%d missing=%v", cost, missing)
			}
		}()
	}
	wg.Wait()
	d.Admission.Close()
	if got := l.QuotaUsed("quota:t", 24*time.Hour); got != 20 {
		t.Fatalf("quota=%d", got)
	}
	w := budget.CalendarMonthIn(time.UTC)
	if got := b.Spent(budget.Key(budget.ScopeTeam, "t", w), w); got != 20 {
		t.Fatalf("spend=%d", got)
	}
	if got := l.RateUsed("tpm:t", 1000, 1000); got != 20 {
		t.Fatalf("rate=%d", got)
	}
}

func TestClosedOrUncertainAdmissionCannotRefundEstimate(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		l := limiter.NewMemory()
		g := NewGovernor(map[string]TeamPolicy{"t": {TokensPerMinute: 1000}}, l, budget.NewMemory(), nil)
		d := g.PreCheck(Subject{Team: "t"}, KeyPolicy{}, 100)
		if !d.Allowed {
			t.Fatal(d)
		}
		if closeFirst {
			d.Admission.Close()
		}
		g.Settle(Subject{Team: "t"}, KeyPolicy{}, "p", "m", pricing.Usage{Input: 1, Uncertain: true}, testTable(), d.Admission)
		d.Admission.Close()
		if got := l.RateUsed("tpm:t", 1000, 1000); got != 100 {
			t.Fatalf("closed=%v rate=%d", closeFirst, got)
		}
	}
}

func TestAdmissionReconcilesOriginalBucketsAfterPolicyChange(t *testing.T) {
	l := limiter.NewMemory()
	g := NewGovernor(nil, l, budget.NewMemory(), nil)
	policy := TeamPolicy{TokensPerMinute: 1000}
	g.SetTeamLookup(func(string) (TeamPolicy, bool) { return policy, true })
	s := Subject{Team: "t", KeyID: "k"}
	d := g.PreCheck(s, KeyPolicy{TokensPerMinute: 1000}, 100)
	if !d.Allowed {
		t.Fatal(d)
	}
	defer d.Admission.Close()
	policy = TeamPolicy{} // limits removed while the provider request runs
	g.Settle(s, KeyPolicy{}, "p", "m", pricing.Usage{Input: 20}, testTable(), d.Admission)
	for _, key := range []string{"tpm:t", "tpm:key:k"} {
		if got := l.RateUsed(key, 1000, 1000); got != 20 {
			t.Fatalf("%s=%d want 20", key, got)
		}
	}
}

type admissionTrackingLimiter struct {
	limiter.LimiterStore
	pending int
}

func (l *admissionTrackingLimiter) ReserveRate(key string, cost, rate, burst int64) (func(*int64), bool) {
	finish, allowed := l.LimiterStore.ReserveRate(key, cost, rate, burst)
	if !allowed {
		return nil, false
	}
	l.pending++
	var once sync.Once
	return func(actual *int64) { once.Do(func() { finish(actual); l.pending-- }) }, true
}

func TestAdmissionClosesPartialReservationsOnDenial(t *testing.T) {
	for _, failure := range []string{"key-tpm", "team-budget", "user-budget"} {
		t.Run(failure, func(t *testing.T) {
			l := &admissionTrackingLimiter{LimiterStore: limiter.NewMemory()}
			b := budget.NewMemory()
			p := TeamPolicy{TokensPerMinute: 1000}
			kp := KeyPolicy{TokensPerMinute: 1000}
			w := budget.CalendarMonthIn(time.UTC)
			switch failure {
			case "key-tpm":
				kp.TokensPerMinute = 1
			case "team-budget":
				p.BudgetMicrosPerMonth, p.BudgetExceeded = 1, "block"
				b.Debit(budget.Key(budget.ScopeTeam, "t", w), 2, w)
			case "user-budget":
				b.Debit(budget.Key(budget.ScopeUser, "t/u", w), 2, w)
			}
			g := NewGovernor(map[string]TeamPolicy{"t": p}, l, b, nil)
			g.SetUserLookup(func(string, string) (UserPolicy, bool) {
				return UserPolicy{BudgetMicrosPerMonth: 1, BudgetExceeded: "block"}, true
			})
			d := g.PreCheck(Subject{Team: "t", KeyID: "k", User: "u"}, kp, 100)
			if d.Allowed || d.Admission != nil || l.pending != 0 {
				t.Fatalf("decision=%+v pending=%d", d, l.pending)
			}
		})
	}
}
