// Package controlplane is inferplaned's distribution core (ADR-034): it
// holds the GovernancePolicy document set, answers data-plane sync
// heartbeats (policy pull + consumption report + lease renewal + rejection
// report in one round trip), runs the budget-lease ledger, and exposes the
// connected-dataplane version distribution so an operator can check
// coverage before propagating rules that need a newer schema generation.
//
// Inference traffic NEVER passes through here — this is the off-request-path
// half of the split (ADR-031). Ledger state is in-memory in this iteration:
// a control-plane restart re-learns spend from the next heartbeats'
// cumulative reports (they are cumulative precisely so restarts and lost
// heartbeats never lose spend).
package controlplane

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/policystore"
)

// staleAfter is how long after its last heartbeat a data plane is still
// listed by /v1alpha1/dataplanes.
const staleAfter = 10 * time.Minute

// pruneAfter is how long after its last heartbeat a data plane's ledger rows
// are dropped entirely. Its window-scoped spend is forgotten at that point —
// the same thing a window rollover does — which errs permissive by at most
// the dead proxy's own spend; the alternative (keeping it forever) would
// starve grants permanently once local windows roll. The durable ledger with
// control-plane-owned window epochs replaces this trade-off (ADR-034).
const pruneAfter = 24 * time.Hour

// maxRejections caps the per-dataplane rejection ring.
const maxRejections = 100

// Server is the control-plane distribution state and its HTTP handlers.
type Server struct {
	paths    []string
	token    string // shared bearer token; "" = no auth (loopback-only deployments)
	authOpts authOptions

	mu         sync.Mutex
	wire       []v1alpha1.GovernancePolicy
	generation string
	interval   int                     // heartbeat cadence handed to data planes, seconds
	ledger     map[ruleKey]*ruleLedger // one per lease-managed budget rule
	dataplanes map[string]*dpInfo
	files      map[string]time.Time
	now        func() time.Time // injectable clock for tests

	policyStore policystore.Store    // nil ⇒ file-authoritative; PUT/DELETE ⇒ 405
	updated     map[string]time.Time // policy name → store updated_at (nil on the file path)
}

type ruleKey struct{ policy, rule string }

// ruleLedger is the global accounting for one budget rule: cumulative spend
// and cumulative allowance per data plane. remaining = limit − Σspent −
// Σ(outstanding allowance beyond reported spend of OTHER data planes).
type ruleLedger struct {
	team       string
	limitMicro int64
	grantMicro int64
	renew      time.Duration
	hard       bool
	period     v1alpha1.BudgetPeriod
	spent      map[string]int64 // dataplane → cumulative reported µUSD (monotonic)
	allowance  map[string]int64 // dataplane → cumulative granted µUSD
}

type dpInfo struct {
	APIVersions []string           `json:"apiVersions"`
	Generation  string             `json:"generation"`
	LastSeen    time.Time          `json:"lastSeen"`
	Rejections  []policy.Rejection `json:"rejections,omitempty"`
}

// NewServer loads the policy documents (wire form — the control plane may
// hold rules some data planes can't enforce; per-dataplane rejections
// surface that) and builds the ledger. token, when non-empty, is required as
// a Bearer token on every endpoint (see WithOIDC for the SSO alternative).
func NewServer(token, path string, opts ...Option) (*Server, error) {
	s := &Server{
		paths:      []string{path},
		token:      token,
		authOpts:   newAuthOptions(opts),
		ledger:     map[ruleKey]*ruleLedger{},
		dataplanes: map[string]*dpInfo{},
		now:        time.Now,
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload re-reads the policy paths and rebuilds the document set and ledger.
// Spend/allowance survive for rules that still exist (matched by
// policy+rule name); removed rules drop their state.
func (s *Server) Reload() error {
	wire, files, err := policy.LoadWirePaths(s.paths...)
	if err != nil {
		return err
	}
	mtimes := make(map[string]time.Time, len(files))
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			mtimes[f] = info.ModTime()
		}
	}
	return s.applyWire(wire, mtimes)
}

// applyWire installs a document set: it rebuilds the lease ledger (carrying
// spend/allowance forward for every rule whose policy+rule name pair still
// exists), recomputes the heartbeat interval, and swaps in the new set and
// generation. The ONE path both the file loader (Reload) and the policy store
// (ReloadFromStore) go through, so a DB-sourced document set can never get
// different ledger semantics from a file-sourced one.
func (s *Server) applyWire(wire []v1alpha1.GovernancePolicy, mtimes map[string]time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger := map[ruleKey]*ruleLedger{}
	minRenew := time.Duration(0)
	for i := range wire {
		doc := &wire[i]
		internal, err := policy.FromV1Alpha1(doc)
		if err != nil {
			return err // unreachable: LoadWirePaths already validated
		}
		for _, r := range internal.Rules {
			// A USER-scoped budget rule gets no ledger row and no lease
			// (ADR-042 Phase 3). Its limit is one person's, but a ruleLedger
			// is keyed by TEAM (l.team) and its grant clamps every data plane
			// serving that team — so admitting it here would throttle the
			// whole team to an individual's cap. Per-user budget is therefore
			// per-data-plane in-memory only, the same posture `rate` has.
			if r.Budget == nil || internal.Subject.Team == "" || internal.Subject.User != "" {
				continue
			}
			// An explicit "no cap" declaration has nothing to lease — a
			// lease exists to bound local overspend against a real limit,
			// and this rule's LimitMicroUSD/LeaseRenewInterval are both the
			// zero value, not real ones. Leaving it in would let a rule
			// processed after a real budget rule reset minRenew back to 0
			// (any "== 0" rule always wins the minRenew comparison below).
			if r.Budget.Unlimited {
				continue
			}
			k := ruleKey{policy: internal.Name, rule: r.Name}
			l := &ruleLedger{
				team:       internal.Subject.Team,
				limitMicro: r.Budget.LimitMicroUSD,
				grantMicro: r.Budget.LeaseGrantMicroUSD,
				renew:      r.Budget.LeaseRenewInterval,
				hard:       r.Budget.HardCap,
				period:     r.Budget.Period,
				spent:      map[string]int64{},
				allowance:  map[string]int64{},
			}
			if prev, ok := s.ledger[k]; ok {
				l.spent, l.allowance = prev.spent, prev.allowance
			}
			ledger[k] = l
			if minRenew == 0 || r.Budget.LeaseRenewInterval < minRenew {
				minRenew = r.Budget.LeaseRenewInterval
			}
		}
	}
	interval := int(policy.DefaultPolicySyncInterval / time.Second)
	if minRenew > 0 {
		interval = int(minRenew / time.Second)
	}
	if interval < 1 {
		interval = 1
	}

	s.wire, s.generation = wire, policy.GenerationOf(wire)
	s.ledger, s.interval, s.files = ledger, interval, mtimes
	return nil
}

// Watch mtime-polls the policy files like the data plane's local watcher
// (same cadence, same never-fatal posture).
func (s *Server) Watch(ctx interface{ Done() <-chan struct{} }, onErr func(error)) {
	t := time.NewTicker(policy.LocalWatchInterval)
	defer t.Stop()
	var lastErr string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !s.changed() {
				continue
			}
			err := s.Reload()
			if err == nil {
				lastErr = ""
				continue
			}
			if msg := err.Error(); msg != lastErr {
				lastErr = msg
				if onErr != nil {
					onErr(fmt.Errorf("policy reload (keeping previous set): %w", err))
				}
			}
		}
	}
}

func (s *Server) changed() bool {
	s.mu.Lock()
	if s.policyStore != nil {
		s.mu.Unlock()
		return false // DB-authoritative: file mtimes must never reload over a store write
	}
	known := make(map[string]time.Time, len(s.files))
	for f, m := range s.files {
		known[f] = m
	}
	s.mu.Unlock()

	files, err := policy.Enumerate(s.paths...)
	if err != nil || len(files) != len(known) {
		return true
	}
	for _, f := range files {
		prev, ok := known[f]
		if !ok {
			return true
		}
		info, err := os.Stat(f)
		if err != nil || !info.ModTime().Equal(prev) {
			return true
		}
	}
	return false
}

// Mount registers the control-plane endpoints on mux.
func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1alpha1/sync", authn(s.token, s.authOpts, s.handleSync))
	mux.HandleFunc("GET /v1alpha1/dataplanes", authn(s.token, s.authOpts, s.handleDataplanes))
	s.mountExport(mux)   // GET /v1alpha1/config/export (export.go)
	s.mountPolicies(mux) // GET/PUT/DELETE /v1alpha1/policies (policies.go)
}

// handleSync is the single data-plane heartbeat (ADR-034).
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var req policy.SyncRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad sync request"}`, http.StatusBadRequest)
		return
	}
	if req.Dataplane == "" {
		http.Error(w, `{"error":"dataplane id required"}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	now := s.now()

	// Register/refresh the data plane (version distribution view).
	dp, ok := s.dataplanes[req.Dataplane]
	if !ok {
		dp = &dpInfo{}
		s.dataplanes[req.Dataplane] = dp
	}
	dp.APIVersions, dp.Generation, dp.LastSeen = req.APIVersions, req.Generation, now
	dp.Rejections = append(dp.Rejections, req.Rejections...)
	if len(dp.Rejections) > maxRejections {
		dp.Rejections = dp.Rejections[len(dp.Rejections)-maxRejections:]
	}

	// Absorb consumption reports. Cumulative counters normally only grow;
	// a DECREASE means the data plane's budget window rolled over (or the
	// process restarted its in-memory counters) — heartbeats come from one
	// sequential loop per data plane, so there are no stale replays to
	// confuse this with. Adopting the lower value and dropping the old
	// allowance closes the re-spend hole a keep-the-max rule would open:
	// the old allowance must not carry into the fresh window.
	for _, rep := range req.Reports {
		if l, ok := s.ledger[ruleKey{policy: rep.Policy, rule: rep.Rule}]; ok {
			if rep.SpentMicroUSD < l.spent[req.Dataplane] {
				l.allowance[req.Dataplane] = 0
			}
			l.spent[req.Dataplane] = rep.SpentMicroUSD
		}
	}

	// Prune data planes not seen for pruneAfter: drop their ledger rows and
	// registration. Restart churn is the common case — mayu's default
	// instance id is per-boot, so every restart strands a row; without
	// release+prune those strandings would starve grants forever.
	for id, dp := range s.dataplanes {
		if now.Sub(dp.LastSeen) > pruneAfter {
			delete(s.dataplanes, id)
			for _, l := range s.ledger {
				delete(l.spent, id)
				delete(l.allowance, id)
			}
		}
	}

	// Grant leases: every lease-managed rule gets one, allowance =
	// reported spend + a slice of what remains globally. Another data
	// plane's allowance counts as outstanding ONLY while its lease can
	// still be valid (last heartbeat within the 3×renew expiry horizon):
	// an expired lease's unspent grant is money its holder may no longer
	// legally spend, so it is released back to the pool instead of
	// permanently shrinking everyone's remaining budget.
	resp := policy.SyncResponse{Generation: s.generation, SyncIntervalSeconds: s.interval}
	for k, l := range s.ledger {
		var globalSpent, outstanding int64
		for _, sp := range l.spent {
			globalSpent += sp
		}
		// Outstanding iterates the ALLOWANCE map, not the spent map: a
		// freshly granted data plane that has never reported yet has no
		// spent entry, and skipping its grant here would hand the same
		// remaining budget to every newcomer at once.
		for d, al := range l.allowance {
			if d == req.Dataplane {
				continue
			}
			other, known := s.dataplanes[d]
			if !known || now.Sub(other.LastSeen) > 3*l.renew {
				continue // lease expired (or holder pruned): grant released
			}
			if extra := al - l.spent[d]; extra > 0 {
				outstanding += extra
			}
		}
		remaining := l.limitMicro - globalSpent - outstanding
		if remaining < 0 {
			remaining = 0
		}
		add := l.grantMicro
		if add > remaining {
			add = remaining
		}
		allowance := l.spent[req.Dataplane] + add
		l.allowance[req.Dataplane] = allowance
		resp.Leases = append(resp.Leases, policy.LeaseGrant{
			Policy: k.policy, Rule: k.rule, Team: l.team,
			AllowanceMicroUSD: allowance,
			// 3× renew tolerates two missed heartbeats before the
			// rule's failurePolicy takes over.
			ExpiresAt: now.Add(3 * l.renew),
			HardCap:   l.hard,
			Period:    l.period,
		})
	}
	if req.Generation != s.generation {
		resp.Policies = s.wire
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&resp)
}

// handleDataplanes lists recently-seen data planes with the API versions
// they support, the generation they enforce, and their rejections — the
// operator's pre-propagation coverage check.
func (s *Server) handleDataplanes(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	now := s.now()
	// DEEP-copy the entries: encoding happens after the lock is released,
	// and handleSync mutates these structs concurrently — handing the
	// encoder shared pointers would be a data race (PR #50 review finding).
	out := make(map[string]dpInfo, len(s.dataplanes))
	for id, dp := range s.dataplanes {
		if now.Sub(dp.LastSeen) <= staleAfter {
			out[id] = dpInfo{
				APIVersions: append([]string(nil), dp.APIVersions...),
				Generation:  dp.Generation,
				LastSeen:    dp.LastSeen,
				Rejections:  append([]policy.Rejection(nil), dp.Rejections...),
			}
		}
	}
	body := map[string]any{
		"generation":  s.generation,
		"apiVersions": policy.SupportedAPIVersions,
		"dataplanes":  out,
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
