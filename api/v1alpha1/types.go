// Package v1alpha1 holds the versioned wire types of the inferplane config
// API — the contract between the control plane (inferplaned) and the
// node-local data plane (mayu).
//
// The schema borrows the Kubernetes resource shape (apiVersion / kind /
// metadata / spec) so documents are kubectl-friendly and CNCF-idiomatic, but
// DELIVERY is inferplane's own gRPC/HTTP channel: workstation mode has no
// K8s API server, so nothing here may depend on Kubernetes machinery
// (ADR-031). v1alpha1 is unstable — fields may change without notice until
// v1beta1.
//
// Version-skew stance: a data plane that receives a document it does not
// fully understand must reject it explicitly and report the rejection to the
// control plane — silent ignoring is the worst failure mode a governance
// tool can have. The rejection logic lives in internal/policy, which both
// binaries import; this package is types only.
package v1alpha1

// APIVersion identifies this schema generation on the wire.
const APIVersion = "inferplane.dev/v1alpha1"

// KindGovernancePolicy is the kind of the rule-set document.
const KindGovernancePolicy = "GovernancePolicy"

// TypeMeta mirrors the Kubernetes resource envelope.
type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

// ObjectMeta names a document and tracks its control-plane edit generation.
type ObjectMeta struct {
	Name string `json:"name"`
	// Generation increments on every control-plane edit; the data plane
	// echoes it in consumption reports so operators can see which revision
	// each proxy is enforcing before propagating a new one.
	Generation int64 `json:"generation,omitempty"`
}

// GovernancePolicy is a versioned set of governance rules distributed from
// the control plane to data planes.
type GovernancePolicy struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta           `json:"metadata"`
	Spec     GovernancePolicySpec `json:"spec"`
}

// GovernancePolicySpec carries the rules of one policy document.
type GovernancePolicySpec struct {
	Rules []Rule `json:"rules"`
}

// FailurePolicy is what a rule does when the control plane is unreachable
// and the rule's lease (if any) has expired. It is PER RULE, never global:
// a global fail-open voids budget control, a global fail-closed voids the
// fault-isolation argument for a node-local data plane.
type FailurePolicy string

const (
	// FailOpen keeps serving when the control plane is unreachable.
	FailOpen FailurePolicy = "FailOpen"
	// FailClosed blocks when the rule can no longer be enforced locally.
	FailClosed FailurePolicy = "FailClosed"
)

// Rule is one governance rule. Exactly one of the kind-specific fields
// (Budget, Routing) is set. FailurePolicy is REQUIRED — there is no default,
// because a defaulted failure mode is a silent one.
type Rule struct {
	Name          string        `json:"name"`
	FailurePolicy FailurePolicy `json:"failurePolicy"`
	Budget        *BudgetRule   `json:"budget,omitempty"`
	Routing       *RoutingRule  `json:"routing,omitempty"`
}

// BudgetRule enforces spend via budget leases: the control plane grants the
// data plane a slice of budget it may burn without a network round trip, the
// data plane reports consumption asynchronously and renews (§ lease pattern,
// ADR-031). Cost is integer microUSD — never float.
type BudgetRule struct {
	// LimitMicroUSD is the global limit this rule enforces across all
	// data planes for the budget window.
	LimitMicroUSD int64 `json:"limitMicroUSD"`
	// HardCap marks the limit as inviolable. A hard-cap rule MUST carry
	// FailurePolicy=FailClosed: when its lease expires and the control
	// plane is unreachable, serving stops. Soft budgets fail open.
	HardCap bool `json:"hardCap,omitempty"`
	// Lease sizes the local-enforcement grant. No defaults are provided
	// yet — issuance unit and renewal cadence are the parameters that
	// trade budget-overshoot tolerance against control-plane load, and
	// their defaults are an open decision (★1, ADR-031). Both fields are
	// therefore required.
	Lease LeaseSpec `json:"lease"`
}

// LeaseSpec sizes a budget lease. Both fields are required; defaults are
// deliberately not chosen yet (★1, ADR-031).
type LeaseSpec struct {
	// GrantMicroUSD is how much budget one lease grants a data plane.
	GrantMicroUSD int64 `json:"grantMicroUSD"`
	// RenewInterval is how often the data plane reports consumption and
	// renews, as a Go duration string (e.g. "30s").
	RenewInterval string `json:"renewInterval"`
}

// ConflictPreference resolves the collision between cache-affinity routing
// and provider fallback: failing over to another region/profile cold-starts
// the server-side prompt cache and can RAISE cost. The preference is
// explicit per rule — neither side wins by default.
type ConflictPreference string

const (
	// PreferAffinity keeps the session pinned to its cache-warm target
	// even when fallback would otherwise fire.
	PreferAffinity ConflictPreference = "PreferAffinity"
	// PreferFallback lets fallback move the session, accepting the
	// server-cache cold start.
	PreferFallback ConflictPreference = "PreferFallback"
)

// RoutingRule pins sessions/prefixes to targets to keep server-side prompt
// caches warm (cache-affinity routing).
type RoutingRule struct {
	// OnAffinityConflict is REQUIRED: what to do when fallback wants to
	// move a session that cache affinity wants to keep pinned.
	OnAffinityConflict ConflictPreference `json:"onAffinityConflict"`
}
