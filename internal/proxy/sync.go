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

	"github.com/inferplane/inferplane/internal/policy"
)

// Lease is the data-plane view of one team's budget lease, merged across
// rules most-restrictive-first.
type Lease struct {
	AllowanceMicroUSD int64
	ExpiresAt         time.Time
	HardCap           bool
}

// LeaseTable is the request-path view of current leases. Reads are on the
// hot path (governor team lookup + lease gate); writes happen once per
// heartbeat.
type LeaseTable struct {
	mu     sync.RWMutex
	byTeam map[string]Lease
}

func NewLeaseTable() *LeaseTable {
	return &LeaseTable{byTeam: map[string]Lease{}}
}

// Get returns the merged lease for a team, if any.
func (t *LeaseTable) Get(team string) (Lease, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	l, ok := t.byTeam[team]
	return l, ok
}

// Blocked implements the governor's lease gate (ADR-034). A HARD-cap team
// fails closed when its lease EXPIRED (the global budget can no longer be
// verified locally) or its allowance is zero (the global budget is already
// exhausted before this data plane spent anything — nothing to clamp to,
// since 0 means "unlimited" in TeamPolicy). A valid lease with a positive
// allowance never blocks here — that bound is enforced by the budget check
// itself (the team-lookup closure clamps the limit to the allowance). Soft
// (non-hard-cap) leases never block: control-plane outage fails open for
// them, per-rule failurePolicy.
func (t *LeaseTable) Blocked(team string) (bool, string) {
	t.mu.RLock()
	l, ok := t.byTeam[team]
	t.mu.RUnlock()
	if !ok || !l.HardCap {
		return false, ""
	}
	if time.Now().After(l.ExpiresAt) {
		return true, "budget lease expired (control plane unreachable): hard cap fails closed"
	}
	if l.AllowanceMicroUSD <= 0 {
		return true, "global hard budget exhausted: no lease allowance remaining"
	}
	return false, ""
}

// set replaces the table from one heartbeat's grants, merging per team
// most-restrictive-first: smallest allowance binds, hard if any rule is
// hard, earliest expiry wins.
func (t *LeaseTable) set(grants []policy.LeaseGrant) {
	byTeam := make(map[string]Lease, len(grants))
	for _, g := range grants {
		l, ok := byTeam[g.Team]
		if !ok {
			byTeam[g.Team] = Lease{AllowanceMicroUSD: g.AllowanceMicroUSD, ExpiresAt: g.ExpiresAt, HardCap: g.HardCap}
			continue
		}
		if g.AllowanceMicroUSD < l.AllowanceMicroUSD {
			l.AllowanceMicroUSD = g.AllowanceMicroUSD
		}
		if g.ExpiresAt.Before(l.ExpiresAt) {
			l.ExpiresAt = g.ExpiresAt
		}
		l.HardCap = l.HardCap || g.HardCap
		byTeam[g.Team] = l
	}
	t.mu.Lock()
	t.byTeam = byTeam
	t.mu.Unlock()
}

// Syncer runs the heartbeat loop against inferplaned.
type Syncer struct {
	URL       string // control plane base URL
	Token     string // shared bearer token; "" = none
	Dataplane string // stable instance id
	Store     *policy.Store
	Leases    *LeaseTable
	// SpentOf reports a team's cumulative window spend in µUSD (wired to
	// the governor's usage view).
	SpentOf func(team string) int64
	// OnError receives loop errors (logged by the caller); never fatal —
	// control-plane outage must not take the data plane down.
	OnError func(error)

	client     *http.Client
	generation string
	pending    []policy.Rejection
}

// Run heartbeats until ctx is done. The first sync fires immediately so a
// freshly booted data plane picks up policy without waiting a full cadence;
// afterwards the control plane's requested interval paces the loop.
// Failures keep the last-applied policy set and leases (their expiry is what
// eventually flips hard caps to fail-closed) and retry next tick.
func (s *Syncer) Run(ctx context.Context) {
	interval := time.Duration(policy.MinPolicySyncInterval)
	if next, err := s.syncOnce(ctx); err != nil {
		if s.OnError != nil {
			s.OnError(err)
		}
	} else {
		interval = next
	}
	t := time.NewTimer(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if next, err := s.syncOnce(ctx); err != nil {
				if s.OnError != nil {
					s.OnError(err)
				}
			} else {
				interval = next
			}
			t.Reset(interval)
		}
	}
}

// syncOnce does one heartbeat and returns the next cadence.
func (s *Syncer) syncOnce(ctx context.Context) (time.Duration, error) {
	if s.client == nil {
		s.client = &http.Client{Timeout: 10 * time.Second}
	}
	req := policy.SyncRequest{
		Dataplane:   s.Dataplane,
		APIVersions: policy.SupportedAPIVersions,
		Generation:  s.generation,
		Rejections:  s.pending,
	}
	// Cumulative spend per lease-managed budget rule of the APPLIED set.
	for _, p := range s.Store.Policies() {
		if p.Subject.Team == "" {
			continue
		}
		for _, r := range p.Rules {
			if r.Budget == nil {
				continue
			}
			var spent int64
			if s.SpentOf != nil {
				spent = s.SpentOf(p.Subject.Team)
			}
			req.Reports = append(req.Reports, policy.ConsumptionReport{
				Policy: p.Name, Rule: r.Name, Team: p.Subject.Team, SpentMicroUSD: spent,
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
		s.Leases.set(resp.Leases)
	}

	next := time.Duration(resp.SyncIntervalSeconds) * time.Second
	if next < time.Second {
		next = policy.DefaultPolicySyncInterval
	}
	return next, nil
}
