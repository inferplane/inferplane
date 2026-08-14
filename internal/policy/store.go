package policy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// LocalWatchInterval is the mtime-poll cadence for locally loaded policy
// files: save → applied within ~2s, the workstation "edit and it's live"
// loop. Distinct from DefaultPolicySyncInterval, which paces the
// control-plane reconcile poll.
const LocalWatchInterval = 2 * time.Second

// TeamLimits is the merged, enforceable budget/rate view of every
// team-subject policy matching one team, in the units the governance
// pipeline consumes (µUSD). Zero means "unlimited" on that dimension,
// mirroring governance.TeamPolicy.
type TeamLimits struct {
	RPM                  int64
	TPM                  int64
	BudgetMicrosPerMonth int64
	// BudgetHard reports whether the binding budget rule is a hard cap:
	// the caller maps it to on_exceeded=block (else warn).
	BudgetHard bool
	// AdminContact carries the binding budget rule's contact hint through
	// verbatim (see api/v1alpha1.BudgetRule.AdminContact); empty if unset.
	AdminContact string
}

// Store holds the data plane's currently-loaded local policy set behind an
// atomic snapshot: lookups on the request path never lock, and a reload
// swaps the whole set at once (the same generation posture as live.Holder,
// ADR-006). A failed reload keeps the previous snapshot serving.
//
// Enforceability gate: this data plane build can enforce team-subject budget
// and rate rules (via the Governor's team lookup) and team- or user-subject
// modelAccess rules (via the Router's policy gate). Anything else — routing
// rules, user-subject budget/rate — is REJECTED at load with an explicit
// *UnsupportedError, never silently accepted-and-ignored (the version-skew
// stance). The gates lift as the corresponding enforcement lands.
type Store struct {
	paths []string
	snap  atomic.Pointer[snapshot]
}

type snapshot struct {
	policies []*Policy
	files    map[string]time.Time // watched file → mtime at load
	teams    map[string]TeamLimits
}

// NewStore loads the given paths (files or directories) and fails on any
// parse, validation, or enforceability error — a data plane must not start
// while claiming to enforce a policy set it can't. Boot-time posture; use
// Reload/Watch afterwards.
func NewStore(paths ...string) (*Store, error) {
	s := &Store{paths: paths}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewEmptyStore builds a store with no policies, to be fed by a control
// plane via ApplyWire (ADR-034). It has no file paths — Reload/Watch are
// meaningless on it and must not be used.
func NewEmptyStore() *Store {
	s := &Store{}
	s.snap.Store(&snapshot{teams: map[string]TeamLimits{}})
	return s
}

// ApplyWire replaces the policy set from control-plane-distributed wire
// documents. Unlike the all-or-nothing file path (an operator's local edit
// should fail fast and whole), distribution applies PER DOCUMENT: schema
// errors and rules this build cannot enforce reject that one document, the
// rest apply, and every rejection is returned for the next heartbeat's
// report — refused loudly upstream, never silently dropped (the version-skew
// stance). Atomic swap: readers see either the old set or the new one.
func (s *Store) ApplyWire(docs []v1alpha1.GovernancePolicy) []Rejection {
	var accepted []*Policy
	var rejected []Rejection
	seen := make(map[string]bool, len(docs))
	for i := range docs {
		name := docs[i].Metadata.Name
		if name == "" || seen[name] {
			rejected = append(rejected, Rejection{Policy: name, Reason: "metadata.name missing or duplicate in distributed set"})
			continue
		}
		seen[name] = true
		p, err := FromV1Alpha1(&docs[i])
		if err == nil {
			err = checkEnforceable(p)
		}
		if err != nil {
			rej := Rejection{Policy: name, Reason: err.Error()}
			var ue *UnsupportedError
			if errors.As(err, &ue) {
				rej.Rule = ue.Rule
				rej.Reason = ue.Reason
			}
			rejected = append(rejected, rej)
			continue
		}
		accepted = append(accepted, p)
	}
	s.snap.Store(&snapshot{
		policies: accepted,
		teams:    mergeTeamLimits(accepted),
	})
	return rejected
}

// Reload re-reads every configured path and atomically swaps the snapshot.
// On error the previous snapshot (if any) stays in force.
func (s *Store) Reload() error {
	// Runtime guard, not just a doc comment (PR #50 review finding): on a
	// control-plane-fed store (NewEmptyStore) a stray generic Reload —
	// e.g. from a future SIGHUP handler — would otherwise silently WIPE
	// the distributed policy set with an empty file scan.
	if len(s.paths) == 0 {
		return errors.New("policy store has no file paths (control-plane-fed): Reload/Watch do not apply")
	}
	policies, files, err := LoadPaths(s.paths...)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if err := checkEnforceable(p); err != nil {
			return err
		}
	}

	mtimes := make(map[string]time.Time, len(files))
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			mtimes[f] = info.ModTime()
		}
	}
	s.snap.Store(&snapshot{
		policies: policies,
		files:    mtimes,
		teams:    mergeTeamLimits(policies),
	})
	return nil
}

// checkEnforceable rejects rules this data plane build cannot enforce yet.
func checkEnforceable(p *Policy) error {
	reject := func(rule, reason string) error {
		return &UnsupportedError{APIVersion: SupportedAPIVersions[0], Kind: "GovernancePolicy", Rule: rule, Reason: reason}
	}
	for _, r := range p.Rules {
		if r.Routing != nil {
			return reject(r.Name, "routing rules are not yet enforceable by this data plane build")
		}
		// Budget/rate enforcement windows are team-keyed today, so ANY
		// user-scoped variant — user-only or (team, user) — must be refused,
		// not accepted-and-ignored. A (team, user) subject would otherwise
		// pass validation, be skipped by mergeTeamLimits, and enforce
		// nothing: the exact silent failure this gate exists to prevent.
		if (r.Budget != nil || r.Rate != nil) && (p.Subject.Team == "" || p.Subject.User != "") {
			return reject(r.Name, "budget/rate rules require a team-only subject in this build (user-scoped budget/rate are not yet enforceable; user subjects currently support modelAccess only)")
		}
	}
	return nil
}

// Watch polls the loaded files' mtimes every LocalWatchInterval and reloads
// on any change (including a file appearing in a watched directory — the
// directory listing is re-walked on every reload). Reload errors keep the
// old snapshot and are passed to onErr; they do not stop the watch. Returns
// when ctx is done.
func (s *Store) Watch(ctx context.Context, onErr func(error)) {
	if len(s.paths) == 0 {
		if onErr != nil {
			onErr(errors.New("policy store has no file paths (control-plane-fed): Watch does not apply"))
		}
		return
	}
	t := time.NewTicker(LocalWatchInterval)
	defer t.Stop()
	var lastErr string // a broken file stays broken across polls; log it once, not every 2s
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

// changed reports whether any watched file's mtime moved, a file vanished,
// or a watched directory gained a policy file.
func (s *Store) changed() bool {
	snap := s.snap.Load()
	files, err := enumerate(s.paths...)
	if err != nil || len(files) != len(snap.files) {
		return true
	}
	for _, f := range files {
		prev, ok := snap.files[f]
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

// Policies returns the current snapshot's policy set (read-only).
func (s *Store) Policies() []*Policy {
	return s.snap.Load().policies
}

// TeamLimits returns the merged budget/rate limits of every team-subject
// policy matching team, and whether any matched.
func (s *Store) TeamLimits(team string) (TeamLimits, bool) {
	tl, ok := s.snap.Load().teams[team]
	return tl, ok
}

// ModelAllowed reports whether every modelAccess rule matching the request's
// team and user allows the (already-canonicalized) model. ALL matching rules
// must allow — most-restrictive-wins across team- and user-subject policies
// (ADR-032). No matching modelAccess rule means no policy restriction.
// Allow-list entries are canonicalized through canon before comparing, the
// same alias posture as Router.Allows (ADR-021); "*" allows every model.
func (s *Store) ModelAllowed(team, user, model string, canon func(string) string) bool {
	for _, p := range s.snap.Load().policies {
		if !p.Subject.matches(team, user) {
			continue
		}
		for _, r := range p.Rules {
			if r.ModelAccess == nil {
				continue
			}
			if !allowsModel(r.ModelAccess.Allow, model, canon) {
				return false
			}
		}
	}
	return true
}

// matches reports whether a subject selects the given request identity. A
// set selector must match; an empty one doesn't constrain. (Validation
// guarantees at least one selector is set.)
func (sub Subject) matches(team, user string) bool {
	if sub.Team != "" && sub.Team != team {
		return false
	}
	if sub.User != "" && (user == "" || sub.User != user) {
		return false
	}
	return true
}

func allowsModel(allow []string, model string, canon func(string) string) bool {
	for _, a := range allow {
		if a == "*" || a == model {
			return true
		}
		if canon != nil && canon(a) == model {
			return true
		}
	}
	return false
}

// mergeTeamLimits folds every team-subject budget/rate rule into one
// TeamLimits per team, most-restrictive-wins: the smallest non-zero limit
// binds each dimension, and the budget is hard if the binding (smallest)
// budget rule — or any equal to it — is a hard cap.
//
// A team gets an entry ONLY when some rule actually contributed a budget or
// rate: a modelAccess-only policy must not manufacture an all-zero (=
// unlimited) entry, which the caller's lookup chain would let shadow the
// team's DB-record/config limits.
func mergeTeamLimits(policies []*Policy) map[string]TeamLimits {
	out := make(map[string]TeamLimits)
	for _, p := range policies {
		if p.Subject.Team == "" || p.Subject.User != "" {
			// Pure team subjects only: user-scoped budget/rate (user-only or
			// (team, user)) is rejected by checkEnforceable.
			continue
		}
		tl, contributed := out[p.Subject.Team]
		for _, r := range p.Rules {
			if r.Rate != nil {
				contributed = true
				tl.RPM = minNonZero(tl.RPM, r.Rate.RPM)
				tl.TPM = minNonZero(tl.TPM, r.Rate.TPM)
			}
			if r.Budget != nil {
				contributed = true
				switch {
				case r.Budget.Unlimited:
					// An explicit "no cap" declaration must never narrow OR
					// widen a binding limit set by another rule — it only
					// prevents this team from implicitly falling through to
					// a config/DB default when it's the ONLY budget rule
					// (handled below: LimitMicroUSD stays 0 in that case,
					// same "no rule" sentinel every consumer already
					// understands). Unlike the branches below, it never
					// compares against or overwrites tl.BudgetMicrosPerMonth.
				case tl.BudgetMicrosPerMonth == 0 || r.Budget.LimitMicroUSD < tl.BudgetMicrosPerMonth:
					tl.BudgetMicrosPerMonth = r.Budget.LimitMicroUSD
					tl.BudgetHard = r.Budget.HardCap
					tl.AdminContact = r.Budget.AdminContact
				case r.Budget.LimitMicroUSD == tl.BudgetMicrosPerMonth && r.Budget.HardCap:
					tl.BudgetHard = true
					if tl.AdminContact == "" {
						tl.AdminContact = r.Budget.AdminContact
					}
				}
			}
		}
		if contributed {
			out[p.Subject.Team] = tl
		}
	}
	return out
}

func minNonZero(a, b int64) int64 {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	case b < a:
		return b
	default:
		return a
	}
}
