package governance

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/inferplane/inferplane/internal/budget"
)

var ErrLocalBudgetExhausted = errors.New("local budget spend or unresolved liability exhausted")

type localMoneyScope struct {
	key   string
	win   budget.Window
	limit int64
	hard  bool
}

type localMoneyState struct {
	ends   time.Time
	held   int64
	frozen bool
}

type localMoneyHold struct {
	state  *localMoneyState
	amount int64
}

// LocalBudgetPermit owns liabilities for one provider attempt. They are not
// observed cost and are never added to the spend ledger or spend metrics.
type LocalBudgetPermit struct {
	once    sync.Once
	holds   []localMoneyHold
	scopes  []localMoneyScope
	subject Subject
}

func (g *Governor) localMoneyScopes(s Subject, kp KeyPolicy) []localMoneyScope {
	if g.HasSharedAuthority() {
		return nil
	}
	var scopes []localMoneyScope
	add := func(scope, id string, limit int64, mode string, win budget.Window) {
		if limit > 0 {
			scopes = append(scopes, localMoneyScope{budget.Key(scope, id, win), win, limit, mode != "warn"})
		}
	}
	month, day := g.monthWindow(), g.dayWindow()
	if p, ok := g.policyOf(s.Team); ok {
		add(budget.ScopeTeam, s.Team, p.BudgetMicrosPerMonth, p.BudgetExceeded, month)
		add(budget.ScopeTeam, s.Team, p.BudgetMicrosPerDay, p.BudgetDayExceeded, day)
	}
	add(budget.ScopeKey, s.KeyID, kp.BudgetMicrosPerMonth, "", month)
	add(budget.ScopeKey, s.KeyID, kp.BudgetMicrosPerDay, "", day)
	if s.User != "" && g.userLookup != nil {
		if p, ok := g.userLookup(s.Team, s.User); ok {
			add(budget.ScopeUser, userBudgetID(s), p.BudgetMicrosPerMonth, p.BudgetExceeded, month)
			add(budget.ScopeUser, userBudgetID(s), p.BudgetMicrosPerDay, p.BudgetExceeded, day)
		}
	}
	return scopes
}

func (g *Governor) HasLocalBudget(s Subject, kp KeyPolicy) bool {
	if g == nil {
		return false
	}
	return len(g.localMoneyScopes(s, kp)) != 0
}

// ReserveLocalBudget atomically claims every blocking local money scope.
// Admission retains the local profile's one-request overshoot contract: an
// attempt may start while spend+liability is below the cap, then its full bound
// prevents overlapping attempts from multiplying that overshoot. Without a
// verified finite price/window, reserve all remaining authority; an unknown
// result freezes that scope until its calendar window ends.
func (g *Governor) ReserveLocalBudget(s Subject, kp KeyPolicy, bound int64, bounded bool) (*LocalBudgetPermit, error) {
	scopes := g.localMoneyScopes(s, kp)
	if len(scopes) == 0 {
		return nil, nil
	}
	if bound < 0 {
		return nil, ErrLocalBudgetExhausted
	}
	g.localMoneyMu.Lock()
	defer g.localMoneyMu.Unlock()
	if g.localMoney == nil {
		g.localMoney = make(map[string]*localMoneyState)
	}
	// Bound the auxiliary ledger as well as the underlying spend store.
	if len(g.localMoney)+len(scopes) > 100_000 {
		for key, state := range g.localMoney {
			if !time.Now().Before(state.ends) || (state.held == 0 && !state.frozen) {
				delete(g.localMoney, key)
			}
		}
		if len(g.localMoney)+len(scopes) > 100_000 {
			return nil, ErrLocalBudgetExhausted
		}
	}
	permit := &LocalBudgetPermit{scopes: scopes, subject: s}
	for _, scope := range scopes {
		if !scope.hard {
			continue
		}
		ends := g.bud.ResetsAt(scope.key, scope.win)
		state := g.localMoney[scope.key]
		if state == nil || !state.ends.Equal(ends) {
			state = &localMoneyState{ends: ends}
			g.localMoney[scope.key] = state
		}
		spent := g.bud.Spent(scope.key, scope.win)
		if state.frozen || spent >= scope.limit || state.held >= scope.limit-spent ||
			g.bud.Check(scope.key, state.held, scope.limit, scope.win) != budget.Allow {
			return nil, ErrLocalBudgetExhausted
		}
		amount := bound
		if !bounded {
			amount = scope.limit - spent
		}
		permit.holds = append(permit.holds, localMoneyHold{state, amount})
	}
	for _, hold := range permit.holds {
		if hold.amount > math.MaxInt64-hold.state.held {
			hold.state.held = math.MaxInt64
			hold.state.frozen = true
		} else {
			hold.state.held += hold.amount
		}
	}
	return permit, nil
}

// FinishLocalBudget releases only proved complete priced usage. For partial or
// unknown results the reserved liability remains. A malformed price/usage
// observation freezes the scope rather than pretending its finite bound held.
func (g *Governor) FinishLocalBudget(p *LocalBudgetPermit, actual *int64, complete, invalid bool, provider, model string) {
	if p == nil {
		return
	}
	p.once.Do(func() {
		func() {
			g.localMoneyMu.Lock()
			defer g.localMoneyMu.Unlock()
			// Debit before releasing holds, so another admission never sees a gap
			// between a provider's completed usage and its financial liability.
			// A failed debit keeps every hold; unlocking on panic still allows
			// future admission to observe the conservative outstanding liability.
			if actual != nil && *actual >= 0 {
				for _, scope := range p.scopes {
					g.bud.Debit(scope.key, *actual, scope.win)
				}
			}
			for _, hold := range p.holds {
				if complete && !invalid {
					hold.state.held -= hold.amount
				} else if invalid {
					hold.state.frozen = true
				}
			}
		}()
		if actual != nil && *actual >= 0 {
			g.metrics.AddBudgetSpend(p.subject.Team, model, "total", float64(*actual)/1e6)
			for _, scope := range p.scopes {
				if scope.win.Kind != budget.CalMonth {
					continue
				}
				spent := g.bud.Spent(scope.key, scope.win)
				switch scope.key {
				case budget.Key(budget.ScopeTeam, p.subject.Team, scope.win):
					g.metrics.SetBudgetUtilization(p.subject.Team, float64(spent)/float64(scope.limit))
					if g.notifyBudget != nil {
						g.notifyBudget(p.subject.Team, spent, scope.limit)
					}
				case budget.Key(budget.ScopeKey, p.subject.KeyID, scope.win):
					if g.notifyKeyBudget != nil {
						g.notifyKeyBudget(p.subject.Team, p.subject.KeyID, spent, scope.limit)
					}
				}
			}
		} else if invalid {
			g.metrics.IncPricingMiss(provider, model)
		}
	})
}

func (g *Governor) localReserved(key string, win budget.Window) (int64, bool) {
	ends := g.bud.ResetsAt(key, win)
	g.localMoneyMu.Lock()
	defer g.localMoneyMu.Unlock()
	if state := g.localMoney[key]; state != nil && state.ends.Equal(ends) {
		return state.held, state.frozen
	}
	return 0, false
}
