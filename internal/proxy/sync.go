package proxy

// This file implements mayu's control-plane heartbeat (ADR-034): one POST
// per cadence carries the policy pull, cumulative consumption report, lease
// renewal, and version-skew rejection report. The request path never waits
// on this loop — enforcement reads the lease table and policy store, both
// atomic snapshots.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/tier"
)

// Lease is the data-plane view of one team's budget lease for one window,
// merged across that window's rules most-restrictive-first.
type Lease struct {
	AllowanceMicroUSD int64
	ExpiresAt         time.Time
	HardCap           bool
	// WindowID is the control-plane window epoch this allowance applies to
	// (roadmap ②; "" from a pre-epoch control plane). BaselineMicroUSD is
	// this data plane's LOCAL cumulative counter reading at the moment the
	// epoch last CHANGED: the local counter rolls at the operator-timezone
	// calendar boundary while the control plane's window is UTC, so during
	// the skew gap the counter still carries old-window spend. Reports send
	// (counter − baseline) so old spend never books into the fresh window,
	// and the gateway's lease clamp allows (allowance + baseline) so the
	// fresh window's grant isn't consumed by spend the old window already
	// settled. Baseline moves only on an OBSERVED epoch change (never on
	// first sight — mid-window attach keeps today's conservative full
	// report) and resets to 0 when the local counter itself rolls below it.
	WindowID         string
	BaselineMicroUSD int64
}

// LeaseTable is the request-path view of current leases, keyed by
// (team, budget window). Reads are on the hot path (governor team lookup +
// lease gate); writes happen once per heartbeat. The nested map keeps
// Blocked to one hash lookup plus a walk of that team's ≤2 windows instead
// of a scan of every team in the fleet on every request.
type LeaseTable struct {
	mu     sync.RWMutex
	byTeam map[string]map[v1alpha1.BudgetPeriod]Lease
}

func NewLeaseTable() *LeaseTable {
	return &LeaseTable{byTeam: map[string]map[v1alpha1.BudgetPeriod]Lease{}}
}

// Get returns the merged lease for one team's budget window, if any. An empty
// period reads as CalendarMonth — what every grant meant before LeaseGrant
// carried a window.
func (t *LeaseTable) Get(team string, period v1alpha1.BudgetPeriod) (Lease, bool) {
	if period == "" {
		period = v1alpha1.PeriodCalendarMonth
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	l, ok := t.byTeam[team][period]
	return l, ok
}

// Snapshot returns a copy of every team's current leases keyed by budget
// window — the operator's debug view (`GET /admin/debug/governance`), never
// the request path.
func (t *LeaseTable) Snapshot() map[string]map[v1alpha1.BudgetPeriod]Lease {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]map[v1alpha1.BudgetPeriod]Lease, len(t.byTeam))
	for team, windows := range t.byTeam {
		cp := make(map[v1alpha1.BudgetPeriod]Lease, len(windows))
		for p, l := range windows {
			cp[p] = l
		}
		out[team] = cp
	}
	return out
}

// Blocked implements the governor's lease gate (ADR-034). A HARD-cap team
// fails closed when its lease EXPIRED (the global budget can no longer be
// verified locally) or its allowance is zero (the global budget is already
// exhausted before this data plane spent anything — nothing to clamp to,
// since 0 means "unlimited" in TeamPolicy). A valid lease with a positive
// allowance never blocks here — that bound is enforced by the budget check
// itself (the team-lookup closure clamps the limit to the allowance). Soft
// (non-hard-cap) leases never block: control-plane outage fails open for
// them, per-rule failurePolicy. It is TEAM-WIDE across windows: if any one
// window's hard-cap lease is expired or exhausted the team is blocked,
// because a hard cap on either window is a cap the data plane can no longer
// verify locally. The windows are walked in a fixed order (day, then month)
// rather than by map range, so the reason string is deterministic.
func (t *LeaseTable) Blocked(team string) (bool, string) {
	t.mu.RLock()
	windows := t.byTeam[team]
	day, dayOK := windows[v1alpha1.PeriodCalendarDay]
	month, monthOK := windows[v1alpha1.PeriodCalendarMonth]
	t.mu.RUnlock()
	now := time.Now()
	for _, e := range [...]struct {
		l  Lease
		ok bool
	}{{day, dayOK}, {month, monthOK}} {
		if !e.ok || !e.l.HardCap {
			continue
		}
		if now.After(e.l.ExpiresAt) {
			return true, "budget lease expired (control plane unreachable): hard cap fails closed"
		}
		if e.l.AllowanceMicroUSD <= 0 {
			return true, "global hard budget exhausted: no lease allowance remaining"
		}
	}
	return false, ""
}

// set replaces the table from one heartbeat's grants, merging per (team,
// window) most-restrictive-first: smallest allowance binds, hard if any rule
// is hard, earliest expiry wins. Merging never crosses windows — a daily
// allowance and a monthly allowance are not comparable quantities. spentOf
// (nil-safe) reads the LOCAL cumulative counter, sampled ONCE per (team,
// window) at the moment a grant's WindowID differs from the table's — the
// roadmap ② epoch baseline (see Lease); a same-epoch heartbeat carries the
// existing baseline forward untouched.
func (t *LeaseTable) set(grants []policy.LeaseGrant, spentOf func(team string, period v1alpha1.BudgetPeriod) int64) {
	byTeam := make(map[string]map[v1alpha1.BudgetPeriod]Lease, len(grants))
	for _, g := range grants {
		// A control plane that predates BudgetRule.period sends no period at
		// all; reading that as CalendarMonth is what keeps the existing wire
		// meaning byte-identical.
		period := g.Period
		if period == "" {
			period = v1alpha1.PeriodCalendarMonth
		}
		windows := byTeam[g.Team]
		if windows == nil {
			windows = map[v1alpha1.BudgetPeriod]Lease{}
			byTeam[g.Team] = windows
		}
		l, ok := windows[period]
		if !ok {
			nl := Lease{AllowanceMicroUSD: g.AllowanceMicroUSD, ExpiresAt: g.ExpiresAt, HardCap: g.HardCap, WindowID: g.WindowID}
			// Baseline: carry the previous table's forward within the same
			// epoch; snapshot the local counter on an OBSERVED epoch change.
			// First sight of a (team, window) — or of an epoch id at all —
			// keeps baseline 0: today's conservative full-report behavior.
			if prev, had := t.Get(g.Team, period); had {
				switch {
				case g.WindowID == "" || prev.WindowID == g.WindowID:
					nl.BaselineMicroUSD = prev.BaselineMicroUSD
				case prev.WindowID != "":
					if spentOf != nil {
						nl.BaselineMicroUSD = spentOf(g.Team, period)
					}
				}
			}
			windows[period] = nl
			continue
		}
		if g.AllowanceMicroUSD < l.AllowanceMicroUSD {
			l.AllowanceMicroUSD = g.AllowanceMicroUSD
		}
		if g.ExpiresAt.Before(l.ExpiresAt) {
			l.ExpiresAt = g.ExpiresAt
		}
		l.HardCap = l.HardCap || g.HardCap
		windows[period] = l
	}
	t.mu.Lock()
	t.byTeam = byTeam
	t.mu.Unlock()
}

// resetBaseline zeroes one window's epoch baseline — called by the report
// loop when the LOCAL counter itself rolled below the baseline (the
// operator-timezone boundary passed), at which point the baseline's spend
// no longer exists in the counter to subtract.
func (t *LeaseTable) resetBaseline(team string, period v1alpha1.BudgetPeriod) {
	if period == "" {
		period = v1alpha1.PeriodCalendarMonth
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if l, ok := t.byTeam[team][period]; ok {
		l.BaselineMicroUSD = 0
		t.byTeam[team][period] = l
	}
}

// Syncer runs the heartbeat loop against inferplaned.
type Syncer struct {
	URL       string // control plane base URL
	Token     string // shared bearer token; "" = none
	Dataplane string // stable instance id
	Store     *policy.Store
	Leases    *LeaseTable
	// Tiers is the request-path ADR-041 budget-tier substitution table,
	// kept in step with every heartbeat's resp.ActiveTiers the same way
	// Leases tracks resp.Leases. nil = no substitution applied.
	Tiers *tier.Table
	// Shares is the ADR-043 rate-share table, applied from every
	// heartbeat's resp.RateShares. nil = no share clamp (per-plane rate
	// enforcement, the pre-share behavior).
	Shares *ShareTable
	// SpentOf reports a team's cumulative spend in µUSD for ONE budget window
	// (wired to the governor's usage view). The period argument is load-bearing:
	// reporting monthly spend against a daily rule's ledger row would starve
	// that rule's grant to zero within hours, because the control plane computes
	// remaining = dayLimit − reportedSpend.
	SpentOf func(team string, period v1alpha1.BudgetPeriod) int64
	// OnError receives loop errors (logged by the caller); never fatal —
	// control-plane outage must not take the data plane down.
	OnError func(error)
	// Version is this build's version, reported in every heartbeat
	// (roadmap ③ phase 1 — fleet version visibility). "" is fine: the
	// control plane shows the plane as version-unknown.
	Version string
	// OnUpdateAdvice fires when the control plane judges this build below
	// its configured fleet minimum — once per DISTINCT advice, not per
	// heartbeat, so a 10s cadence doesn't turn one stale binary into a log
	// flood. Advice only: nothing here fetches or applies an update.
	OnUpdateAdvice func(policy.UpdateAdvice)

	client     *http.Client
	generation string
	pending    []policy.Rejection
	lastAdvice *policy.UpdateAdvice
}

// Run heartbeats until ctx is done. The first sync fires immediately so a
// freshly booted data plane picks up policy without waiting a full cadence;
// afterwards the control plane's requested interval paces the loop.
// Failures keep the last-applied policy set and leases (their expiry is what
// eventually flips hard caps to fail-closed) and retry next tick.
func (s *Syncer) Run(ctx context.Context) {
	interval := s.tick(ctx, policy.MinPolicySyncInterval)
	t := time.NewTimer(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			interval = s.tick(ctx, interval)
			t.Reset(interval)
		}
	}
}

// tick runs one heartbeat and returns the next cadence. Failures back off
// exponentially (doubling up to DefaultPolicySyncInterval) so a fleet of
// data planes doesn't hammer an already-degraded control plane in lockstep
// (PR #50 review finding); the first success snaps back to the control
// plane's requested cadence.
func (s *Syncer) tick(ctx context.Context, prev time.Duration) time.Duration {
	next, err := s.syncOnce(ctx)
	if err == nil {
		return next
	}
	if s.OnError != nil {
		s.OnError(err)
	}
	backoff := prev * 2
	if backoff < policy.MinPolicySyncInterval {
		backoff = policy.MinPolicySyncInterval
	}
	if backoff > policy.DefaultPolicySyncInterval {
		backoff = policy.DefaultPolicySyncInterval
	}
	return backoff
}

// syncOnce does one heartbeat and returns the next cadence.
func (s *Syncer) syncOnce(ctx context.Context) (time.Duration, error) {
	if s.client == nil {
		s.client = &http.Client{Timeout: 10 * time.Second}
	}
	req := policy.SyncRequest{
		Dataplane:   s.Dataplane,
		APIVersions: policy.SupportedAPIVersions,
		Version:     s.Version,
		Generation:  s.generation,
		Rejections:  s.pending,
	}
	// Cumulative spend per lease-managed budget rule of the APPLIED set.
	//
	// TODO(per-rule spend): SpentOf reads ONE team-level counter, so a team
	// with budget rules in several policies reports the same cumulative
	// spend against each rule — the ledger then under-grants every rule
	// beyond the tightest (conservative, never permissive). Per-rule spend
	// tracking lands with the durable-ledger milestone (ADR-034 known
	// limits). The per-WINDOW half of this is now fixed — SpentOf answers
	// for the rule's own window — so only the several-rules-in-one-window
	// case remains conservative.
	for _, p := range s.Store.Policies() {
		// A user-scoped budget rule has no ledger row upstream (ADR-042
		// Phase 3), so reporting the TEAM's spend against it would be
		// reporting the wrong quantity to a row that does not exist.
		if p.Subject.Team == "" || p.Subject.User != "" {
			continue
		}
		for _, r := range p.Rules {
			if r.Budget == nil {
				continue
			}
			var spent int64
			if s.SpentOf != nil {
				spent = s.SpentOf(p.Subject.Team, r.Budget.Period)
			}
			// Window epochs (roadmap ②): report spend relative to the epoch
			// baseline, stamped with the epoch id from the last grant, so
			// spend the OLD window settled never books into the fresh one.
			// A local counter below its baseline means the local calendar
			// boundary passed too — the baseline's spend is gone from the
			// counter, so it resets and the raw value is window-correct again.
			var windowID string
			if s.Leases != nil {
				if l, ok := s.Leases.Get(p.Subject.Team, r.Budget.Period); ok {
					windowID = l.WindowID
					if spent < l.BaselineMicroUSD {
						s.Leases.resetBaseline(p.Subject.Team, r.Budget.Period)
					} else {
						spent -= l.BaselineMicroUSD
					}
				}
			}
			req.Reports = append(req.Reports, policy.ConsumptionReport{
				Policy: p.Name, Rule: r.Name, Team: p.Subject.Team, SpentMicroUSD: spent,
				Period: r.Budget.Period, WindowID: windowID,
			})
		}
	}

	body, err := json.Marshal(&req)
	if err != nil {
		return 0, fmt.Errorf("control plane sync: encode: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+"/v1alpha1/sync", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("control plane sync: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		hreq.Header.Set("Authorization", "Bearer "+s.Token)
	}
	hresp, err := s.client.Do(hreq)
	if err != nil {
		return 0, fmt.Errorf("control plane sync: %w", err)
	}
	defer hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(hresp.Body, 4096))
		return 0, fmt.Errorf("control plane sync: status %d", hresp.StatusCode)
	}
	var resp policy.SyncResponse
	if err := json.NewDecoder(io.LimitReader(hresp.Body, 8<<20)).Decode(&resp); err != nil {
		return 0, fmt.Errorf("control plane sync: decode: %w", err)
	}

	// The heartbeat delivered the pending rejections; new ones may replace
	// them below when a fresh document set arrives.
	s.pending = nil
	if resp.Policies != nil {
		rejected := s.Store.ApplyWire(resp.Policies)
		s.pending = rejected
		s.generation = resp.Generation
	}
	if s.Leases != nil {
		s.Leases.set(resp.Leases, s.SpentOf)
	}
	if s.Tiers != nil {
		s.Tiers.Set(resp.ActiveTiers)
	}
	if s.Shares != nil {
		s.Shares.Set(resp.RateShares)
	}
	if resp.UpdateAdvice != nil && s.OnUpdateAdvice != nil &&
		(s.lastAdvice == nil || *s.lastAdvice != *resp.UpdateAdvice) {
		s.OnUpdateAdvice(*resp.UpdateAdvice)
	}
	s.lastAdvice = resp.UpdateAdvice

	next := time.Duration(resp.SyncIntervalSeconds) * time.Second
	if next < time.Second {
		next = policy.DefaultPolicySyncInterval
	}
	return next, nil
}
