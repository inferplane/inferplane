// Package debugapi serves GET /admin/debug/governance (roadmap ④, the
// remote half of `mayu doctor`): a secret-free snapshot of this data
// plane's governance state — policy source, per-team usage counters, the
// budget-lease table (ADR-034), and rate shares (ADR-043) — so an operator
// can pull the same picture doctor shows locally from a machine they
// cannot shell into.
//
// Leakage posture matches /admin/config: no secret, no key_id, no owner in
// the DTO by default — usage is reported for TEAM subjects only, which the
// governance layer already keeps key- and user-free.
package debugapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// Snapshot is the whole response. Teams is keyed by team name (a
// config/policy-declared value, not client input).
type Snapshot struct {
	// PolicySource is "files", "control_plane", or "none".
	PolicySource string          `json:"policy_source"`
	Teams        map[string]Team `json:"teams,omitempty"`
}

// Team is one team's governance state.
type Team struct {
	// Usage is the governor's read-only usage view (the same shape
	// GET /v1/usage serves the team's own keys), team subject only.
	Usage any `json:"usage,omitempty"`
	// Leases are the ADR-034 budget leases currently held for the team,
	// one per budget window.
	Leases []Lease `json:"leases,omitempty"`
	// Share is the ADR-043 rate share currently clamping the team's
	// policy rate, if a control plane has delivered one.
	Share *Share `json:"share,omitempty"`
}

// Lease mirrors proxy.Lease plus its window, JSON-shaped for operators.
type Lease struct {
	Period             string    `json:"period"`
	AllowanceUSDMicros int64     `json:"allowance_usd_micros"`
	ExpiresAt          time.Time `json:"expires_at"`
	HardCap            bool      `json:"hard_cap"`
}

// Share mirrors proxy.Share.
type Share struct {
	RPM int64 `json:"rpm,omitempty"`
	TPM int64 `json:"tpm,omitempty"`
}

// Handler serves the snapshot. The closure is built by the gateway, which
// owns the governor, lease table, and share table; nil closures are mounted
// as 404 by the caller instead.
func Handler(snapshot func() Snapshot) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot())
	})
}
