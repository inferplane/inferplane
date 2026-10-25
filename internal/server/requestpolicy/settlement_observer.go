package requestpolicy

import (
	"context"
	"net/http"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/pkg/schema"
)

type settlementObserverKey struct{}
type attemptObserverKey struct{}

// WithSettlementObserver is an in-process, request-scoped observation hook for
// the administrator evaluation bridge. It cannot alter admission or settlement.
// Callbacks run synchronously; no usage or credentials enter a global registry.
func WithSettlementObserver(ctx context.Context, observe func(*audit.CostRef, *schema.Usage, bool)) context.Context {
	return context.WithValue(ctx, settlementObserverKey{}, observe)
}

func observeSettlement(req *http.Request, cost *audit.CostRef, usage *schema.Usage, complete bool) {
	if observe, ok := req.Context().Value(settlementObserverKey{}).(func(*audit.CostRef, *schema.Usage, bool)); ok && observe != nil {
		observe(cost, usage, complete)
	}
}

// WithAttemptObserver counts explicit gateway attempts. Comparing these with
// settlements prevents a successful fallback from concealing an earlier
// possibly billable failure. It does not grant financial authority.
func WithAttemptObserver(ctx context.Context, observe func()) context.Context {
	return context.WithValue(ctx, attemptObserverKey{}, observe)
}

func observeAttempt(req *http.Request) {
	if observe, ok := req.Context().Value(attemptObserverKey{}).(func()); ok && observe != nil {
		observe()
	}
}
