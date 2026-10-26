package governance

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/limiter"
)

func TestLocalMoneyConcurrentOvershootIsOneAttempt(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{"t": {BudgetMicrosPerMonth: 100}}, limiter.NewMemory(), budget.NewMemory(), nil)
	var accepted atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := g.ReserveLocalBudget(Subject{Team: "t"}, KeyPolicy{}, 200, true); err == nil {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("overlapping attempts accepted=%d, want 1", got)
	}
}

func TestLocalMoneyInvalidObservationExposesFrozenState(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{"t": {BudgetMicrosPerMonth: 100}}, limiter.NewMemory(), budget.NewMemory(), nil)
	s := Subject{Team: "t"}
	p, err := g.ReserveLocalBudget(s, KeyPolicy{}, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	g.FinishLocalBudget(p, nil, false, true, "p", "m")
	status := g.UsageOf(s, KeyPolicy{}).TeamBudget
	if status.RemainingUSDMicros != 0 {
		t.Fatalf("frozen accounting must not advertise available money: %+v", status)
	}
}

func TestLocalMoneyRejectIsAtomicAcrossTeamAndKey(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{"t": {BudgetMicrosPerMonth: 100}}, limiter.NewMemory(), budget.NewMemory(), nil)
	s := Subject{Team: "t", KeyID: "k"}
	kp := KeyPolicy{BudgetMicrosPerDay: 1}
	g.bud.Debit(budget.Key(budget.ScopeKey, "k", g.dayWindow()), 1, g.dayWindow())
	if _, err := g.ReserveLocalBudget(s, kp, 20, true); err == nil {
		t.Fatal("exhausted key day must refuse")
	}
	if held := g.UsageOf(s, kp).TeamBudget.ReservedUSDMicros; held != 0 {
		t.Fatalf("refused attempt left team reservation=%d", held)
	}
}

func TestLocalMoneyConcurrentFinalizersDebitExactlyOnce(t *testing.T) {
	g := NewGovernor(map[string]TeamPolicy{"t": {BudgetMicrosPerMonth: 100}}, limiter.NewMemory(), budget.NewMemory(), nil)
	s := Subject{Team: "t"}
	p, err := g.ReserveLocalBudget(s, KeyPolicy{}, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	actual := int64(10)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.FinishLocalBudget(p, &actual, true, false, "p", "m")
		}()
	}
	wg.Wait()
	u := g.UsageOf(s, KeyPolicy{}).TeamBudget
	if u.ReservedUSDMicros != 0 || u.SpentUSDMicros != 10 {
		t.Fatalf("concurrent finalization charged repeatedly: %+v", u)
	}
}
