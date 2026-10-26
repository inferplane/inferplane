package governance

import (
	"sync"
	"sync/atomic"
)

// Admission belongs to one successful PreCheck. Its identity is the handle, not
// a user-controlled request ID or an unbounded global deduplication map.
// Close must be deferred immediately after an allowed PreCheck, including on
// panic, cancellation and missing usage. Unproved usage retains the estimate.
// Legacy ingresses share an admission across pre-response retries; Responses
// performs PreCheck separately for each attempt.
type Admission struct {
	once       sync.Once
	rates      []func(*int64)
	estimate   int64
	cost       int64
	missing    bool
	moneyOwned bool
	dispatched atomic.Bool
}

func (a *Admission) finishRates(actual *int64) {
	if a == nil {
		return
	}
	for _, finish := range a.rates {
		finish(actual)
	}
}

func (a *Admission) Close() {
	if a != nil {
		a.once.Do(func() { a.finishRates(nil) })
	}
}

// OwnLocalMoney leaves monetary settlement to the per-provider-attempt permit.
// Rate reservations still belong to the original admission.
func (a *Admission) OwnLocalMoney() {
	if a != nil {
		a.moneyOwned = true
	}
}

func (a *Admission) Cancel() {
	if a != nil {
		zero := int64(0)
		a.once.Do(func() { a.finishRates(&zero) })
	}
}

// MarkDispatched records that an attempt passed the last admission gate.
// Legacy retries share this handle, so a later refusal cannot refund the
// original attempt's unresolved token reservation.
func (a *Admission) MarkDispatched() {
	if a != nil {
		a.dispatched.Store(true)
	}
}

func (a *Admission) CancelUndispatched() {
	if a != nil && !a.dispatched.Load() {
		a.Cancel()
	}
}
