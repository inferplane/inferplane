package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// Sync protocol (ADR-034): one data-plane heartbeat carries the policy pull,
// the consumption report, the lease renewal, and the version-skew rejection
// report in a single round trip — "report + renew" is one call, so a data
// plane is never mid-conversation with the control plane on the request path.
//
// Unit note: this is the MACHINE channel, so money is integer microUSD
// (exact), not the operator-facing milliUSD of policy documents (ADR-032).
// Consumption is sub-milliUSD per request; reporting it coarser would lose
// spend the same way settling coarser would (ADR-030).

// SyncRequest is what a data plane POSTs to the control plane each heartbeat.
type SyncRequest struct {
	// Dataplane is this proxy's stable instance id.
	Dataplane string `json:"dataplane"`
	// APIVersions is what this data-plane build understands — the control
	// plane exposes the distribution so an operator can check coverage
	// before propagating rules that need a newer generation.
	APIVersions []string `json:"apiVersions"`
	// Version is this data-plane build's version (roadmap ③ phase 1:
	// fleet version visibility). Additive/omitempty: an older build simply
	// doesn't report one, and the control plane shows it as unknown.
	Version string `json:"version,omitempty"`
	// Generation is the policy-set generation last applied ("" = none);
	// the control plane omits the policy payload when it matches.
	Generation string `json:"generation,omitempty"`
	// Rejections reports documents the data plane refused since the last
	// heartbeat — explicit version-skew reporting, never silent.
	Rejections []Rejection `json:"rejections,omitempty"`
	// Reports carries cumulative spend per lease-managed budget rule.
	Reports []ConsumptionReport `json:"reports,omitempty"`
}

// Rejection is one refused policy document (or rule), reported upstream.
type Rejection struct {
	Policy string `json:"policy"`
	Rule   string `json:"rule,omitempty"`
	Reason string `json:"reason"`
}

// ConsumptionReport is cumulative spend for one budget rule's team in the
// rule's own window (see Period), in µUSD. Cumulative (not delta) so a lost
// heartbeat never loses spend; a DECREASE tells the control plane the
// reporter's budget window rolled over (or its counters restarted), and the
// ledger adopts the fresh counter rather than keeping the old maximum.
type ConsumptionReport struct {
	Policy        string `json:"policy"`
	Rule          string `json:"rule"`
	Team          string `json:"team"`
	SpentMicroUSD int64  `json:"spentMicroUSD"`
	// Period is the budget window SpentMicroUSD was measured against.
	// Appended last with omitempty: a data plane that predates
	// BudgetRule.period omits it, and the control plane reads that as
	// CalendarMonth — exactly the meaning the field had implicitly before it
	// existed. The ledger still MATCHES a report on policy+rule (unique per
	// rule) — Period is never part of that lookup key — but the control
	// plane DOES compare it against the matched rule's current period and
	// skips absorbing a report whose Period disagrees (a lagging data plane,
	// or a heartbeat landing right after an in-place period edit): the
	// number would otherwise be booked in the wrong window's currency.
	Period v1alpha1.BudgetPeriod `json:"period,omitempty"`
	// WindowID echoes the LeaseGrant.WindowID this spend was measured
	// against (roadmap ② window epochs). Appended last with omitempty: an
	// older build reports none, and the control plane falls back to the
	// decrease-detection heuristic for that plane. When present and NOT the
	// rule's current window, the report is skipped — spend from a previous
	// epoch must never book into the fresh window.
	WindowID string `json:"windowID,omitempty"`
}

// SyncResponse is the control plane's answer.
type SyncResponse struct {
	Generation string `json:"generation"`
	// Policies is the full current document set; omitted (nil) when the
	// caller's generation already matches.
	Policies []v1alpha1.GovernancePolicy `json:"policies,omitempty"`
	// Leases carries one budget grant per lease-managed rule.
	Leases []LeaseGrant `json:"leases,omitempty"`
	// ActiveTiers carries the currently-active budget tier (ADR-041) for
	// every budgetTiers routing rule whose referenced budget rule has
	// crossed at least its lowest threshold, judged GLOBALLY from the
	// control plane's lease ledger (the same Σ spend + Σ outstanding grants
	// the lease loop above computes) and latched per budget window: once
	// active, a tier stays active until the window rolls over, never
	// flapping and never escalating/lifting on stale (disconnected) data —
	// see internal/tier.Latch. Additive/omitempty: an older data plane build
	// that doesn't understand it simply never applies a substitution.
	ActiveTiers []ActiveTier `json:"activeTiers,omitempty"`
	// SyncIntervalSeconds is the control plane's requested heartbeat
	// cadence (derived from the tightest lease renew interval).
	SyncIntervalSeconds int `json:"syncIntervalSeconds"`
	// UpdateAdvice is present when the control plane's configured
	// minimumVersion judges THIS data plane's reported version stale (or
	// unparseable, e.g. a "dev" build). Advice only — nothing auto-applies;
	// the control plane must never be able to push executable content
	// (roadmap ③, the security constraint). Additive/omitempty.
	UpdateAdvice *UpdateAdvice `json:"updateAdvice,omitempty"`
	// RateShares carries THIS data plane's slice of each team rate rule's
	// global rpm/tpm (ADR-043). Additive/omitempty: an older build ignores
	// them and keeps enforcing the full policy limit per plane, exactly the
	// pre-share behavior.
	RateShares []RateShare `json:"rateShares,omitempty"`
}

// RateShare is one data plane's slice of a team rate rule's global limit
// (ADR-043): the control plane divides the rule's rpm/tpm among live data
// planes so the fleet aggregate stays bounded by the configured limit. The
// receiving plane clamps its locally-enforced limit to min(policy limit,
// share) — a share can only narrow, never widen. A zero dimension means the
// rule does not limit it. ExpiresAt is observability only in v1: failure
// semantics are keep-last (FailOpen — a rate limit protects throughput,
// not money).
type RateShare struct {
	Policy    string    `json:"policy"`
	Rule      string    `json:"rule"`
	Team      string    `json:"team"`
	RPM       int64     `json:"rpm,omitempty"`
	TPM       int64     `json:"tpm,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// UpdateAdvice tells a stale data plane the fleet's minimum version and
// where to fetch a newer build. It is advisory by design — see the
// SyncResponse field comment.
type UpdateAdvice struct {
	MinVersion string `json:"minVersion"`
	URL        string `json:"url,omitempty"`
}

// ActiveTier is the currently-active budget tier for one budgetTiers routing
// rule: at least ThresholdPercent utilization of Policy/BudgetRef's budget
// rule has been reached, and Substitute is the tier's requested-model ->
// target map to apply at ingress.
type ActiveTier struct {
	Policy           string            `json:"policy"`
	Rule             string            `json:"rule"`
	BudgetRef        string            `json:"budgetRef"`
	Team             string            `json:"team"`
	ThresholdPercent int               `json:"thresholdPercent"`
	Substitute       map[string]string `json:"substitute"`
}

// LeaseGrant is one budget lease: the data plane may serve this rule's team
// until cumulative local spend reaches AllowanceMicroUSD, with no network
// round trip, until ExpiresAt.
type LeaseGrant struct {
	Policy string `json:"policy"`
	Rule   string `json:"rule"`
	Team   string `json:"team"`
	// AllowanceMicroUSD is CUMULATIVE window allowance for this data plane
	// (reported spend + fresh grant), not an increment — idempotent across
	// retried heartbeats.
	AllowanceMicroUSD int64     `json:"allowanceMicroUSD"`
	ExpiresAt         time.Time `json:"expiresAt"`
	// HardCap mirrors the rule: an expired hard-cap lease fails closed.
	HardCap bool `json:"hardCap"`
	// Period is the budget window this allowance applies to. Appended last
	// with omitempty: a control plane that predates BudgetRule.period omits
	// it and the data plane reads it as CalendarMonth, which is what every
	// grant meant before windows existed. The data plane keys its lease table
	// by (team, period) — a daily allowance and a monthly allowance are not
	// comparable quantities and must never be merged into one minimum.
	Period v1alpha1.BudgetPeriod `json:"period,omitempty"`
	// WindowID is the control-plane-computed budget window epoch this
	// allowance applies to (roadmap ②): "2026-09" for a CalendarMonth rule,
	// "2026-09-02" for a CalendarDay rule, both UTC — the control plane
	// owns the window, so rollover is a deliberate epoch change, not a
	// heuristic inferred from a decreasing counter. The data plane echoes
	// it in ConsumptionReport and baselines its local counter when the
	// epoch changes. Appended last with omitempty: an older control plane
	// sends none and the data plane behaves exactly as before epochs.
	WindowID string `json:"windowID,omitempty"`
}

// GenerationOf fingerprints a policy document set: the sha256 of the
// canonical JSON encoding, order-sensitive (documents are loaded in sorted
// file order, so the same set hashes the same everywhere). The FULL digest
// is used: a truncated fingerprint colliding would silently suppress a
// policy update forever, since a matching generation skips the payload
// (PR #50 review finding).
func GenerationOf(docs []v1alpha1.GovernancePolicy) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for i := range docs {
		_ = enc.Encode(&docs[i])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// LoadWirePaths reads GovernancePolicy documents from files/dirs (same path
// semantics as LoadPaths) and returns the WIRE form — what a control plane
// distributes verbatim. Every document is still schema-validated through
// FromV1Alpha1 (the control plane must not distribute garbage), but the
// data-plane enforceability gate is deliberately NOT applied here: the
// control plane may hold rules some data planes can't enforce yet — that is
// exactly what per-dataplane rejection reporting exists to surface.
func LoadWirePaths(paths ...string) ([]v1alpha1.GovernancePolicy, []string, error) {
	files, err := enumerate(paths...)
	if err != nil {
		return nil, nil, err
	}
	var out []v1alpha1.GovernancePolicy
	seen := make(map[string]string)
	for _, f := range files {
		data, err := readPolicyFile(f)
		if err != nil {
			return nil, nil, err
		}
		docs, err := parseWireDocs(data)
		if err != nil {
			return nil, nil, fmt.Errorf("policy file %s: %w", f, err)
		}
		for _, d := range docs {
			if d.Metadata.Name == "" {
				return nil, nil, fmt.Errorf("policy file %s: metadata.name is required", f)
			}
			if prev, dup := seen[d.Metadata.Name]; dup {
				return nil, nil, fmt.Errorf("policy file %s: duplicate policy name %q (already defined in %s)", f, d.Metadata.Name, prev)
			}
			seen[d.Metadata.Name] = f
			out = append(out, d)
		}
	}
	return out, files, nil
}
