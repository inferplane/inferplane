package requestpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
)

type budgetKey struct{}

var errMultiplicity = errors.New("durable budgets require a single generation per request")

type budgetAttempt struct {
	governor           *governance.Governor
	permit             *governance.BudgetPermit
	shared             *governance.SharedPermit
	local              *governance.LocalBudgetPermit
	localInvalid       bool
	subject            governance.Subject
	done               atomic.Bool
	table              *pricing.Table
	provider, upstream string
	window             int64
}

// ReserveBudget commits local escrow before the caller contacts its provider.
// The returned finalizer MUST run even after a panic/cancellation or early error;
// absent a proved complete result it retains the entire reservation as uncertain.
func ReserveBudget(req *http.Request, g *governance.Governor, p keystore.Principal, target router.ChainTarget, state *live.State, raw []byte, admissions ...*governance.Admission) (reservedReq *http.Request, finish func(), reserveErr error) {
	observeAttempt(req)
	defer func() {
		for _, admission := range admissions {
			if reserveErr != nil {
				admission.CancelUndispatched()
			} else {
				admission.MarkDispatched()
			}
		}
	}()
	subject := governance.Subject{Team: p.Team, KeyID: p.KeyID, User: p.AccountSubject()}
	key := governance.KeyPolicy{BudgetMicrosPerMonth: p.BudgetUSDMicros, BudgetMicrosPerDay: p.BudgetUSDMicrosPerDay}
	if !g.HasBudgetAuthority() && !g.HasSharedAuthority() && !g.HasLocalBudget(subject, key) {
		return req, func() {}, nil
	}
	if err := singleGeneration(raw); err != nil {
		return req, nil, err
	}
	if state == nil {
		return req, nil, governance.ErrAuthorityUnavailable
	}
	model, ok := state.Route(target.Model)
	if !ok {
		return req, nil, governance.ErrAuthorityUnavailable
	}
	bound, err := state.Pricing().AuthorityBound(target.ProviderName, target.Upstream, model.ContextWindow, model.ContextWindow)
	if err != nil && (g.HasBudgetAuthority() || g.HasSharedAuthority()) {
		return req, nil, governance.ErrAuthorityUnavailable
	}
	if g.HasSharedAuthority() {
		return reserveShared(req, g, p, target, state, model.ContextWindow, bound)
	}
	local, localErr := g.ReserveLocalBudget(subject, key, bound, err == nil)
	if localErr != nil {
		return req, nil, localErr
	}
	if local != nil {
		for _, admission := range admissions {
			admission.OwnLocalMoney()
		}
	}
	window := model.ContextWindow
	if window <= 0 {
		window = math.MaxInt64
	}
	a := &budgetAttempt{governor: g, local: local, table: state.Pricing(),
		provider: target.ProviderName, upstream: target.Upstream, window: window}
	if !g.HasBudgetAuthority() {
		req = req.WithContext(context.WithValue(req.Context(), budgetKey{}, a))
		return req, func() { a.finish(nil, false) }, nil
	}
	permit, err := g.ReserveBudget(req.Context(), subject, bound)
	if err != nil {
		g.FinishLocalBudget(local, nil, true, false, "", "") // authority refused before dispatch
		return req, nil, err
	}
	if permit == nil {
		g.FinishLocalBudget(local, nil, true, false, "", "")
		return req, nil, governance.ErrAuthorityInvalid
	}
	a.permit = permit
	req = req.WithContext(context.WithValue(req.Context(), budgetKey{}, a))
	return req, func() { a.finish(nil, false) }, nil
}

func (a *budgetAttempt) finish(actual *int64, complete bool) {
	a.finishObserved(actual, actual, complete)
}

// A local partial observation is distinct from the durable authority's exact
// settlement. It cannot prove a refund or replace that authority's uncertainty.
func (a *budgetAttempt) finishObserved(localActual, authorityActual *int64, complete bool) {
	if a.shared != nil {
		a.finishShared(governance.SharedSettlement{CostMicroUSD: authorityActual, Complete: complete})
		return
	}
	if !a.done.CompareAndSwap(false, true) {
		return
	}
	a.governor.FinishLocalBudget(a.local, localActual, complete && localActual != nil, a.localInvalid, a.provider, a.upstream)
	if a.permit == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.governor.FinishBudget(ctx, a.permit, authorityActual, complete); err != nil {
		// Disk/state errors must leave the reservation held, not fall back to
		// memory. Avoid including private journal/capability material in logs.
		log.Print("inferplane: budget authority finalization failed; reservation retained")
	}
}

func BudgetStatus(err error) int {
	var shared *governance.SharedDenial
	if errors.As(err, &shared) {
		return shared.Status
	}
	if errors.Is(err, governance.ErrSharedUnavailable) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, errMultiplicity) {
		return http.StatusBadRequest
	}
	if errors.Is(err, governance.ErrAuthorityExhausted) || errors.Is(err, governance.ErrLocalBudgetExhausted) {
		return http.StatusPaymentRequired
	}
	return http.StatusServiceUnavailable
}

// SettleBudget distinguishes actual observed cost from authority retained for
// uncertain outcomes. Missing/malformed usage can never prove a safe refund.
func SettleBudget(req *http.Request, cost *audit.CostRef, usage *schema.Usage, complete bool) {
	observedComplete := complete
	defer func() { observeSettlement(req, cost, usage, observedComplete) }()
	a, _ := req.Context().Value(budgetKey{}).(*budgetAttempt)
	if a == nil {
		return
	}
	if a.shared != nil {
		observedComplete = a.settleShared(usage, complete)
		return
	}
	u, exact, invalid := authorityUsage(usage, a.window)
	a.localInvalid = invalid
	if a.permit != nil {
		a.permit.InvalidUsage = invalid
	}
	if !exact || invalid {
		observedComplete = false
		a.finishObserved(a.localObservedCost(usage), nil, false)
		return
	}
	actual, err := a.table.CostUSDMicrosChecked(a.provider, a.upstream, u)
	if err != nil {
		observedComplete = false
		a.localInvalid = true
		if a.permit != nil {
			a.permit.InvalidUsage = true
		}
		a.finishObserved(a.localObservedCost(usage), nil, false)
		return
	}
	a.finish(&actual, complete)
}

// localObservedCost bills only categories actually reported with valid counts.
// Missing output and unknown cache TTLs retain the full reservation. Neither a
// flat cache total nor an unknown context tier is evidence of a category price.
func (a *budgetAttempt) localObservedCost(usage *schema.Usage) *int64 {
	if a.local == nil || usage == nil {
		return nil
	}
	observed := false
	valid := func(n *int64) (int64, bool) {
		if n == nil || *n < 0 || *n > a.window {
			return 0, false
		}
		observed = true
		return *n, true
	}
	var u pricing.Usage
	var inputKnown bool
	u.Input, inputKnown = valid(usage.InputTokens)
	u.Output, _ = valid(usage.OutputTokens)
	readKnown := true
	if usage.CacheReadInputTokens != nil {
		u.CacheRead, readKnown = valid(usage.CacheReadInputTokens)
	}
	cache := usage.CacheWriteAccounting()
	if !cache.Invalid && usage.CacheCreation != nil {
		u.CacheWrite5m, _ = valid(usage.CacheCreation.Ephemeral5mInputTokens)
		u.CacheWrite1h, _ = valid(usage.CacheCreation.Ephemeral1hInputTokens)
	}
	if !observed {
		return nil
	}
	// Partial input categories cannot establish a long-context price tier.
	if a.table.ContextTier(a.provider, a.upstream, u) != "" &&
		(!inputKnown || !readKnown || !cache.TTLKnown || cache.Invalid || usage.AccountingUncertain) {
		return nil
	}
	cost, err := a.table.CostUSDMicrosChecked(a.provider, a.upstream, u)
	if err != nil && (u.CacheWrite5m != 0 || u.CacheWrite1h != 0) {
		// A provider can report cache creation for a rate that explicitly
		// cannot price it. Preserve independently priced input/output/read.
		before := a.table.ContextTier(a.provider, a.upstream, u)
		u.CacheWrite5m, u.CacheWrite1h = 0, 0
		if a.table.ContextTier(a.provider, a.upstream, u) != before {
			return nil
		}
		cost, err = a.table.CostUSDMicrosChecked(a.provider, a.upstream, u)
	}
	if err != nil {
		return nil
	}
	return &cost
}

// Inspect only generation controls, never arbitrary tool arguments. The
// supported APIs are single-generation here; opaque vendor extensions must not
// silently multiply output charged against one model window.
func singleGeneration(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errMultiplicity
	}
	for _, key := range []string{"n", "best_of", "num_generations", "num_return_sequences", "candidate_count", "candidateCount"} {
		if value, ok := fields[key]; ok {
			var n int64
			if json.Unmarshal(value, &n) != nil || n != 1 {
				return errMultiplicity
			}
		}
	}
	for _, key := range []string{"inferenceConfig", "textGenerationConfig", "generation_config", "additionalModelRequestFields"} {
		if value, ok := fields[key]; ok {
			if err := singleGeneration(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func authorityUsage(u *schema.Usage, window int64) (pricing.Usage, bool, bool) {
	var out pricing.Usage
	if u == nil {
		return out, false, false
	}
	values := []*int64{u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens}
	if u.CacheCreation != nil {
		values = append(values, u.CacheCreation.Ephemeral5mInputTokens, u.CacheCreation.Ephemeral1hInputTokens)
	}
	for _, v := range values {
		if v != nil && (*v < 0 || *v > window) {
			return out, false, true
		}
	}
	if u.AccountingUncertain {
		return out, false, false
	}
	if u.InputTokens == nil || u.OutputTokens == nil {
		return out, false, false
	}
	out.Input, out.Output = *u.InputTokens, *u.OutputTokens
	if u.CacheReadInputTokens != nil {
		out.CacheRead = *u.CacheReadInputTokens
	}
	total := int64(0)
	if u.CacheCreationInputTokens != nil {
		total = *u.CacheCreationInputTokens
	}
	if u.CacheCreation == nil {
		return out, total == 0, false
	}
	if len(u.CacheCreation.Extra) != 0 {
		return out, false, false
	}
	if u.CacheCreation.Ephemeral5mInputTokens == nil || u.CacheCreation.Ephemeral1hInputTokens == nil {
		return out, false, false // an omitted TTL category cannot prove a refund
	}
	out.CacheWrite5m, out.CacheWrite1h = *u.CacheCreation.Ephemeral5mInputTokens, *u.CacheCreation.Ephemeral1hInputTokens
	if u.CacheCreationInputTokens != nil &&
		(out.CacheWrite5m > total || out.CacheWrite1h != total-out.CacheWrite5m) {
		return out, false, true
	}
	return out, true, false
}
