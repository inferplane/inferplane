// Package policy is the single shared truth for governance rules and budget
// leases between the control plane (cmd/inferplaned) and the data plane
// (cmd/mayu). Both binaries compile against this package, so a schema change
// that only one side understands is caught at compile time instead of
// surfacing as silent version skew in production (ADR-031).
//
// The wire form lives in api/v1alpha1; this package owns conversion into the
// internal form and — critically — explicit rejection: a document the data
// plane does not fully support is refused with an *UnsupportedError meant to
// be reported back to the control plane, never silently dropped.
//
// Unit boundary (ADR-032): the wire speaks integer milliUSD (1000 = $1, the
// operator-facing resolution); this package converts to integer microUSD
// (×1000, exact) because internal cost accounting settles per-token amounts
// that are sub-milliUSD — settling coarser re-opens the ADR-030 zero-cost
// bug class.
package policy

import (
	"fmt"
	"math"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// Cadence decisions (ADR-032). Cost control is near-real-time: worst-case
// budget overshoot is bounded by lease grant × connected data planes, so the
// lease defaults keep grants small and renewals frequent. Policy delivery is
// push-based whenever the control-plane stream is up; the poll interval is
// only the reconcile fallback for disconnected proxies.
const (
	// DefaultLeaseRenewInterval is how often a data plane reports
	// consumption and renews its budget lease when the rule doesn't say.
	DefaultLeaseRenewInterval = 10 * time.Second
	// MinLeaseRenewInterval is the floor for an explicit renew interval.
	MinLeaseRenewInterval = time.Second
	// DefaultLeaseGrantBP is the default lease grant as basis points of
	// the budget limit: 10 bp = 0.1%.
	DefaultLeaseGrantBP = 10
	// DefaultPolicySyncInterval is the reconcile-poll default for policy
	// distribution when the push stream is down.
	DefaultPolicySyncInterval = time.Minute
	// MinPolicySyncInterval is the floor an operator may set the
	// reconcile poll to.
	MinPolicySyncInterval = 15 * time.Second
)

// SupportedAPIVersions lists the config API generations this build
// understands. The control plane exposes the version distribution of
// connected data planes so an operator can check coverage before
// propagating a rule that needs a newer generation.
var SupportedAPIVersions = []string{v1alpha1.APIVersion}

// Supports reports whether this build understands apiVersion.
func Supports(apiVersion string) bool {
	for _, v := range SupportedAPIVersions {
		if v == apiVersion {
			return true
		}
	}
	return false
}

// UnsupportedError is the explicit rejection of a policy document (or one
// rule in it). The data plane MUST surface it to the control plane; silent
// ignoring is the worst failure mode a governance tool can have.
type UnsupportedError struct {
	APIVersion string // apiVersion of the offending document
	Kind       string // kind of the offending document
	Rule       string // rule name, empty when the whole document is rejected
	Reason     string // human-readable reason, safe to report upstream
}

func (e *UnsupportedError) Error() string {
	if e.Rule == "" {
		return fmt.Sprintf("unsupported policy document %s/%s: %s", e.APIVersion, e.Kind, e.Reason)
	}
	return fmt.Sprintf("unsupported rule %q in %s/%s: %s", e.Rule, e.APIVersion, e.Kind, e.Reason)
}

// Policy is the internal, version-independent form of one policy document.
type Policy struct {
	Name       string
	Generation int64
	Subject    Subject
	Rules      []Rule
}

// Subject is who a policy governs: a team (department) and/or a user. User-
// and team-level governance are equal citizens; when several policies match
// one request, the most restrictive outcome wins (block beats warn).
type Subject struct {
	Team string
	User string
}

// Rule is the internal form of one governance rule.
type Rule struct {
	Name          string
	FailurePolicy v1alpha1.FailurePolicy
	Budget        *Budget
	Routing       *Routing
	ModelAccess   *ModelAccess
	Rate          *Rate
}

// Budget is the internal form of a budget rule. All amounts are integer
// microUSD (converted ×1000 from the wire's milliUSD, defaults applied).
type Budget struct {
	LimitMicroUSD      int64
	HardCap            bool
	LeaseGrantMicroUSD int64
	LeaseRenewInterval time.Duration
	AdminContact       string
}

// ModelAccess is the internal form of a model allow-list rule. Entries match
// after alias canonicalization; "*" allows every configured model.
type ModelAccess struct {
	Allow []string
}

// Rate is the internal form of a throughput rule. 0 = unlimited dimension.
type Rate struct {
	RPM int64
	TPM int64
}

// Routing is the internal form of a cache-affinity routing rule.
type Routing struct {
	OnAffinityConflict v1alpha1.ConflictPreference
}

// Lease is one budget grant issued by the control plane to one data plane.
// Within GrantMicroUSD and until ExpiresAt the data plane enforces locally
// with no network round trip; consumption is reported and the lease renewed
// asynchronously. Lease STATE (remaining grant, expiry, counters) persists
// in the data plane's SQLite metadata store across restarts — prompt/response
// payloads never do (they live in the VolatileStore, see internal/cache).
type Lease struct {
	Rule          string
	GrantMicroUSD int64
	ExpiresAt     time.Time
}

// FromV1Alpha1 converts a wire document to the internal form, rejecting
// anything this build does not fully support. It validates the invariants
// the design fixes:
//
//   - the subject must select a team and/or a user;
//   - failurePolicy is required per rule — no silent defaults;
//   - a hard-cap budget rule must be FailClosed (soft budgets fail open);
//   - lease grant / renew interval take the ADR-032 defaults when unset,
//     but an explicit sub-floor renew interval is rejected;
//   - a routing rule must state its affinity-vs-fallback preference;
//   - exactly one rule kind per rule.
func FromV1Alpha1(doc *v1alpha1.GovernancePolicy) (*Policy, error) {
	reject := func(rule, reason string) *UnsupportedError {
		return &UnsupportedError{APIVersion: doc.APIVersion, Kind: doc.Kind, Rule: rule, Reason: reason}
	}
	if !Supports(doc.APIVersion) {
		return nil, reject("", fmt.Sprintf("apiVersion not supported by this build (supported: %v)", SupportedAPIVersions))
	}
	if doc.Kind != v1alpha1.KindGovernancePolicy {
		return nil, reject("", fmt.Sprintf("unknown kind %q", doc.Kind))
	}
	if doc.Spec.Subject.Team == "" && doc.Spec.Subject.User == "" {
		return nil, reject("", "spec.subject must select a team and/or a user")
	}

	p := &Policy{
		Name:       doc.Metadata.Name,
		Generation: doc.Metadata.Generation,
		Subject:    Subject{Team: doc.Spec.Subject.Team, User: doc.Spec.Subject.User},
		Rules:      make([]Rule, 0, len(doc.Spec.Rules)),
	}
	ruleNames := make(map[string]bool, len(doc.Spec.Rules))
	for _, wr := range doc.Spec.Rules {
		if wr.Name == "" {
			return nil, reject("", "rule with empty name")
		}
		if ruleNames[wr.Name] {
			// Rule names key leases and consumption reports later — a
			// duplicate would silently alias two rules' state.
			return nil, reject(wr.Name, "duplicate rule name within one policy")
		}
		ruleNames[wr.Name] = true
		switch wr.FailurePolicy {
		case v1alpha1.FailOpen, v1alpha1.FailClosed:
		case "":
			return nil, reject(wr.Name, "failurePolicy is required; a defaulted failure mode is a silent one")
		default:
			return nil, reject(wr.Name, fmt.Sprintf("unknown failurePolicy %q", wr.FailurePolicy))
		}
		kinds := 0
		for _, set := range []bool{wr.Budget != nil, wr.Routing != nil, wr.ModelAccess != nil, wr.Rate != nil} {
			if set {
				kinds++
			}
		}
		if kinds != 1 {
			return nil, reject(wr.Name, "exactly one of budget, routing, modelAccess, or rate must be set")
		}

		r := Rule{Name: wr.Name, FailurePolicy: wr.FailurePolicy}
		switch {
		case wr.Budget != nil:
			b, err := budgetFromV1Alpha1(wr, reject)
			if err != nil {
				return nil, err
			}
			r.Budget = b
		case wr.Routing != nil:
			switch wr.Routing.OnAffinityConflict {
			case v1alpha1.PreferAffinity, v1alpha1.PreferFallback:
			default:
				return nil, reject(wr.Name, "routing.onAffinityConflict is required: cache affinity vs fallback has no default winner")
			}
			r.Routing = &Routing{OnAffinityConflict: wr.Routing.OnAffinityConflict}
		case wr.ModelAccess != nil:
			if len(wr.ModelAccess.Allow) == 0 {
				return nil, reject(wr.Name, `modelAccess.allow must be non-empty (use ["*"] for all): an empty list is deny-all and must be written deliberately`)
			}
			for _, m := range wr.ModelAccess.Allow {
				if m == "" {
					return nil, reject(wr.Name, "modelAccess.allow contains an empty model name")
				}
			}
			r.ModelAccess = &ModelAccess{Allow: append([]string(nil), wr.ModelAccess.Allow...)}
		case wr.Rate != nil:
			if wr.Rate.RPM < 0 || wr.Rate.TPM < 0 {
				return nil, reject(wr.Name, "rate.rpm and rate.tpm must be >= 0")
			}
			if wr.Rate.RPM == 0 && wr.Rate.TPM == 0 {
				return nil, reject(wr.Name, "rate rule must limit at least one of rpm or tpm")
			}
			r.Rate = &Rate{RPM: wr.Rate.RPM, TPM: wr.Rate.TPM}
		}
		p.Rules = append(p.Rules, r)
	}
	return p, nil
}

// microPerMilli converts wire milliUSD to internal microUSD (exact).
const microPerMilli = 1000

// maxWireMilliUSD is the largest milliUSD amount whose µUSD conversion still
// fits in int64. Anything larger would overflow into a negative internal
// limit — a nonsense figure ($9.2 quadrillion) that can only be a typo, so
// it is rejected rather than clamped.
const maxWireMilliUSD = math.MaxInt64 / microPerMilli

func budgetFromV1Alpha1(wr v1alpha1.Rule, reject func(rule, reason string) *UnsupportedError) (*Budget, error) {
	wb := wr.Budget
	if wb.LimitMilliUSD <= 0 || wb.LimitMilliUSD > maxWireMilliUSD {
		return nil, reject(wr.Name, fmt.Sprintf("budget.limitMilliUSD must be in (0, %d] (1000 = $1)", int64(maxWireMilliUSD)))
	}
	if wb.HardCap && wr.FailurePolicy != v1alpha1.FailClosed {
		return nil, reject(wr.Name, "a hard-cap budget rule must be FailClosed: fail-open on lease expiry voids the cap")
	}

	grantMilli := wb.Lease.GrantMilliUSD
	switch {
	case grantMilli < 0 || grantMilli > maxWireMilliUSD:
		return nil, reject(wr.Name, fmt.Sprintf("budget.lease.grantMilliUSD must be in [0, %d] (0 = default)", int64(maxWireMilliUSD)))
	case grantMilli == 0:
		// ADR-032 default: 0.1% of the limit, floored at 1 milliUSD.
		grantMilli = wb.LimitMilliUSD * DefaultLeaseGrantBP / 10_000
		if grantMilli < 1 {
			grantMilli = 1
		}
	}

	iv := DefaultLeaseRenewInterval
	if wb.Lease.RenewInterval != "" {
		parsed, err := time.ParseDuration(wb.Lease.RenewInterval)
		if err != nil {
			return nil, reject(wr.Name, fmt.Sprintf("budget.lease.renewInterval %q is not a Go duration", wb.Lease.RenewInterval))
		}
		if parsed < MinLeaseRenewInterval {
			return nil, reject(wr.Name, fmt.Sprintf("budget.lease.renewInterval must be >= %s", MinLeaseRenewInterval))
		}
		iv = parsed
	}

	return &Budget{
		LimitMicroUSD:      wb.LimitMilliUSD * microPerMilli,
		HardCap:            wb.HardCap,
		LeaseGrantMicroUSD: grantMilli * microPerMilli,
		LeaseRenewInterval: iv,
		AdminContact:       wb.AdminContact,
	}, nil
}
