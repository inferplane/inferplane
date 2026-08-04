package telemetry

import (
	"fmt"
	"strings"
	"time"
)

// maxEntryValue bounds every per-entry count (µUSD and tokens): large enough
// for any real window ($1B / 10^15 tokens), small enough that summing
// thousands of maxed entries cannot overflow int64.
const maxEntryValue = int64(1e15)

// UsageEntry is one (team, user, model) line of a usage window: settled cost
// in integer µUSD (never float — CLAUDE.md invariant) plus token counts with
// the prompt-cache 5m/1h creation tiers kept separate, because the tiers bill
// at different rates (ADR-030) and collapsing them is exactly the mis-billing
// that split exists to prevent. Model is the RESOLVED model — the name pricing
// billed — not the requested alias.
type UsageEntry struct {
	Team               string `json:"team"`
	User               string `json:"user,omitempty"`
	Model              string `json:"model"`
	SpentMicroUSD      int64  `json:"spent_micro_usd"`
	InputTokens        int64  `json:"input_tokens"`
	OutputTokens       int64  `json:"output_tokens"`
	CacheReadTokens    int64  `json:"cache_read_tokens"`
	CacheWrite5mTokens int64  `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64  `json:"cache_write_1h_tokens"`
}

// UsageBatch is one data plane's usage over one window — the wire body of
// POST /v1alpha1/usage (spec D3). (Dataplane, WindowStart) is the idempotency
// key: the control plane replaces, never accumulates, on re-delivery, so
// mayu's retry buffer can resend a batch safely.
type UsageBatch struct {
	Dataplane   string       `json:"dataplane"`
	WindowStart time.Time    `json:"window_start"`
	WindowEnd   time.Time    `json:"window_end"`
	Entries     []UsageEntry `json:"entries"`
}

// Validate rejects a batch that could never be stored: besides shape errors,
// it catches the two poison-batch classes the P2 gate flagged — an in-batch
// duplicate (team, user, model) key would violate the Postgres primary key,
// and a NUL byte is valid JSON but invalid Postgres TEXT; either would make
// the batch permanently 503 and clog the data plane's retry FIFO forever.
func (b *UsageBatch) Validate() error {
	if b.Dataplane == "" {
		return fmt.Errorf("telemetry: batch dataplane is empty")
	}
	if strings.ContainsRune(b.Dataplane, 0) {
		return fmt.Errorf("telemetry: batch dataplane contains a NUL byte")
	}
	if b.WindowStart.IsZero() || b.WindowEnd.IsZero() {
		return fmt.Errorf("telemetry: batch window bounds are required")
	}
	if !b.WindowStart.Before(b.WindowEnd) {
		return fmt.Errorf("telemetry: batch window_start must precede window_end")
	}
	// Sane bounds: UnixNano is undefined outside ~1678–2262, and a wildly
	// future/past window is clock-skew garbage either way.
	if b.WindowStart.Year() < 2000 || b.WindowEnd.Year() > 2200 {
		return fmt.Errorf("telemetry: batch window outside the sane range (2000–2200)")
	}
	seen := make(map[[3]string]bool, len(b.Entries))
	for i := range b.Entries {
		e := &b.Entries[i]
		if e.Team == "" {
			return fmt.Errorf("telemetry: entry %d: team is empty", i)
		}
		if e.Model == "" {
			return fmt.Errorf("telemetry: entry %d: model is empty", i)
		}
		for name, s := range map[string]string{"team": e.Team, "user": e.User, "model": e.Model} {
			if strings.ContainsRune(s, 0) {
				return fmt.Errorf("telemetry: entry %d: %s contains a NUL byte", i, name)
			}
		}
		for name, n := range map[string]int64{
			"spent_micro_usd": e.SpentMicroUSD, "input_tokens": e.InputTokens,
			"output_tokens": e.OutputTokens, "cache_read_tokens": e.CacheReadTokens,
			"cache_write_5m_tokens": e.CacheWrite5mTokens, "cache_write_1h_tokens": e.CacheWrite1hTokens,
		} {
			if n < 0 {
				return fmt.Errorf("telemetry: entry %d: negative %s", i, name)
			}
			// One window of one team/user/model can't legitimately reach
			// 1e15 (µUSD = $1B; tokens = quadrillions) — a larger value is
			// garbage and, unbounded, could overflow aggregation sums.
			if n > maxEntryValue {
				return fmt.Errorf("telemetry: entry %d: %s exceeds the sane bound", i, name)
			}
		}
		key := [3]string{e.Team, e.User, e.Model}
		if seen[key] {
			return fmt.Errorf("telemetry: entry %d: duplicate (team, user, model) key in batch", i)
		}
		seen[key] = true
	}
	return nil
}
