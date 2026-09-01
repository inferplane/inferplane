package proxy

import (
	"sync"

	"github.com/inferplane/inferplane/internal/policy"
)

// ShareTable holds this data plane's current rate shares (ADR-043), applied
// from each heartbeat's resp.RateShares the same way LeaseTable tracks
// resp.Leases. Failure semantics are keep-last (FailOpen): a control-plane
// outage leaves the last shares in force — never widened back to the global
// limit, never dropped to zero. The consumer (the gateway's team-lookup
// closure) clamps only rate dimensions the policy layer actually declares,
// so a stale entry for a removed rate rule is inert.
type ShareTable struct {
	mu     sync.RWMutex
	byTeam map[string]Share
}

// Share is the effective per-team clamp: when a team has several rate
// rules, the most restrictive share per dimension wins, mirroring
// mergeTeamLimits' most-restrictive-wins for the limits themselves.
// A zero dimension means no rule shares it.
type Share struct {
	RPM, TPM int64
}

// NewShareTable returns an empty table.
func NewShareTable() *ShareTable {
	return &ShareTable{byTeam: map[string]Share{}}
}

// Set folds one heartbeat's shares into the table. Teams absent from the
// batch KEEP their previous share (keep-last, see type comment); teams
// present are replaced wholesale by the batch's most-restrictive fold, so a
// grown share (another plane left the fleet) takes effect immediately.
func (t *ShareTable) Set(shares []policy.RateShare) {
	if len(shares) == 0 {
		return
	}
	fresh := map[string]Share{}
	for _, sh := range shares {
		cur, seen := fresh[sh.Team]
		if !seen {
			fresh[sh.Team] = Share{RPM: sh.RPM, TPM: sh.TPM}
			continue
		}
		cur.RPM = minNonZeroShare(cur.RPM, sh.RPM)
		cur.TPM = minNonZeroShare(cur.TPM, sh.TPM)
		fresh[sh.Team] = cur
	}
	t.mu.Lock()
	for team, sh := range fresh {
		t.byTeam[team] = sh
	}
	t.mu.Unlock()
}

// Get returns the team's current share, if any heartbeat has delivered one.
func (t *ShareTable) Get(team string) (Share, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	sh, ok := t.byTeam[team]
	return sh, ok
}

func minNonZeroShare(a, b int64) int64 {
	if a == 0 {
		return b
	}
	if b == 0 || a < b {
		return a
	}
	return b
}
