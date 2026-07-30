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
package policy

import (
	"fmt"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
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

// Rule is the internal, version-independent form of one governance rule.
type Rule struct {
	Name          string
	FailurePolicy v1alpha1.FailurePolicy
	Budget        *Budget
	Routing       *Routing
}

// Budget is the internal form of a budget rule. Cost is integer microUSD.
type Budget struct {
	LimitMicroUSD      int64
	HardCap            bool
	LeaseGrantMicroUSD int64
	LeaseRenewInterval time.Duration
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

// FromV1Alpha1 converts a wire document to internal rules, rejecting
// anything this build does not fully support. It validates the invariants
// the design fixes:
//
//   - failurePolicy is required per rule — no silent defaults;
//   - a hard-cap budget rule must be FailClosed (soft budgets fail open);
//   - lease grant and renew interval are required and positive — their
//     defaults are an open decision (★1) and must not be invented here;
//   - a routing rule must state its affinity-vs-fallback preference.
func FromV1Alpha1(doc *v1alpha1.GovernancePolicy) ([]Rule, error) {
	reject := func(rule, reason string) *UnsupportedError {
		return &UnsupportedError{APIVersion: doc.APIVersion, Kind: doc.Kind, Rule: rule, Reason: reason}
	}
	if !Supports(doc.APIVersion) {
		return nil, reject("", fmt.Sprintf("apiVersion not supported by this build (supported: %v)", SupportedAPIVersions))
	}
	if doc.Kind != v1alpha1.KindGovernancePolicy {
		return nil, reject("", fmt.Sprintf("unknown kind %q", doc.Kind))
	}

	rules := make([]Rule, 0, len(doc.Spec.Rules))
	for _, wr := range doc.Spec.Rules {
		if wr.Name == "" {
			return nil, reject("", "rule with empty name")
		}
		switch wr.FailurePolicy {
		case v1alpha1.FailOpen, v1alpha1.FailClosed:
		case "":
			return nil, reject(wr.Name, "failurePolicy is required; a defaulted failure mode is a silent one")
		default:
			return nil, reject(wr.Name, fmt.Sprintf("unknown failurePolicy %q", wr.FailurePolicy))
		}
		if (wr.Budget == nil) == (wr.Routing == nil) {
			return nil, reject(wr.Name, "exactly one of budget or routing must be set")
		}

		r := Rule{Name: wr.Name, FailurePolicy: wr.FailurePolicy}
		if wr.Budget != nil {
			b, err := budgetFromV1Alpha1(wr, reject)
			if err != nil {
				return nil, err
			}
			r.Budget = b
		}
		if wr.Routing != nil {
			switch wr.Routing.OnAffinityConflict {
			case v1alpha1.PreferAffinity, v1alpha1.PreferFallback:
			default:
				return nil, reject(wr.Name, "routing.onAffinityConflict is required: cache affinity vs fallback has no default winner")
			}
			r.Routing = &Routing{OnAffinityConflict: wr.Routing.OnAffinityConflict}
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func budgetFromV1Alpha1(wr v1alpha1.Rule, reject func(rule, reason string) *UnsupportedError) (*Budget, error) {
	wb := wr.Budget
	if wb.LimitMicroUSD <= 0 {
		return nil, reject(wr.Name, "budget.limitMicroUSD must be positive")
	}
	if wb.HardCap && wr.FailurePolicy != v1alpha1.FailClosed {
		return nil, reject(wr.Name, "a hard-cap budget rule must be FailClosed: fail-open on lease expiry voids the cap")
	}
	if wb.Lease.GrantMicroUSD <= 0 {
		return nil, reject(wr.Name, "budget.lease.grantMicroUSD is required (no default yet — ★1)")
	}
	iv, err := time.ParseDuration(wb.Lease.RenewInterval)
	if err != nil || iv <= 0 {
		return nil, reject(wr.Name, "budget.lease.renewInterval must be a positive Go duration (no default yet — ★1)")
	}
	return &Budget{
		LimitMicroUSD:      wb.LimitMicroUSD,
		HardCap:            wb.HardCap,
		LeaseGrantMicroUSD: wb.Lease.GrantMicroUSD,
		LeaseRenewInterval: iv,
	}, nil
}
