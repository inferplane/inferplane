package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/policystore"
)

// ErrImportConflict is returned when an import's expected_generation no
// longer matches the distributed set: someone changed policies between the
// operator's dry-run preview and the apply. Surfaces as 409.
var ErrImportConflict = errors.New("policies changed since preview")

// ErrImportNotApplied means the store COMMITTED the bundle but the in-memory
// install refused it — unreachable for a validated bundle, but if it ever
// happens the caller must not read it as "nothing changed". Surfaces as 500.
var ErrImportNotApplied = errors.New("policies were committed to the store but could not be applied in memory")

// Size contract. A data plane decodes at most 8 MiB of sync JSON. Durable
// authority sends the policy set twice. Measure a synthetic superset of both
// sync modes, including repeated identifiers, budgets, leases and the largest
// active-tier maps, with JSON escaping included. Request-driven authority
// items are bounded by the 1024 requests+reports+meters cap in
// internal/authority/pgstore/accounts.go; echoed request strings are covered
// by handleSync's 1 MiB request-body cap. Refuse imports whose measured envelope
// plus these allowances exceeds importMaxSyncBytes. Single-document PUTs can
// already accumulate a set past this bound (a pre-existing gap this import
// check does not close).
// The TRANSPORT bound is only a memory cap:
// block YAML adds per-line indentation that JSON lacks (policy nesting is
// schema-fixed, so the overhead is bounded; a multiline-string fixture
// measured ~4.9x, and newline-per-byte strings at maximum depth stay under
// ~7x), so 64 MiB of YAML admits the export of every set within the fleet
// bound. policyMaxBodyBytes (1 MiB) stays the per-document PUT bound.
const importMaxBodyBytes = 64 << 20

// importMaxSyncBytes is a var only so tests can lower it.
var importMaxSyncBytes = 8 << 20

// errStoreList marks a store READ failure inside ReloadFromStore, as opposed
// to the store holding rows the loader rejects. Import reconciliation treats
// only the former as "store unavailable": a restore must still be able to
// overwrite rejected rows.
var errStoreList = errors.New("list stored policies")

// ImportOptions are the POST /v1alpha1/config/import query parameters.
type ImportOptions struct {
	DryRun             bool
	ExpectedGeneration string
	AllowEmpty         bool
}

// ImportChange is one document's fate under a replace import.
type ImportChange struct {
	Name   string `json:"name"`
	Action string `json:"action"` // added | changed | removed | unchanged
}

// ImportSummary counts ImportChange actions.
type ImportSummary struct {
	Added     int `json:"added"`
	Changed   int `json:"changed"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`
}

// ImportResult is the dry-run preview or the applied outcome.
type ImportResult struct {
	DryRun            bool           `json:"dry_run"`
	CurrentGeneration string         `json:"current_generation"`
	ResultGeneration  string         `json:"result_generation"`
	Changes           []ImportChange `json:"changes"`
	Summary           ImportSummary  `json:"summary"`
	// StoreRejected reports that the store held rows the loader rejects.
	// CurrentGeneration describes the last distributed set, and the diff is
	// against the stored rows.
	StoreRejected bool `json:"store_rejected,omitempty"`
}

// ApplyImport makes body (multi-document GovernancePolicy YAML, the exact
// GET /v1alpha1/config/export format) the COMPLETE policy set — backup
// restore, not merge. Unlike ApplyWrite, the whole bundle is validated
// BEFORE the store is touched: ParseWireDocs runs every document through
// policy.FromV1Alpha1, and names/duplicates/identity subjects are checked
// here, which is every way applyWire can reject a set.
//
// Bundle preparation runs before writeMu. Both preview and apply then hold
// writeMu while reconciling memory with the store and checking the generation,
// so a committed-but-unreloaded PUT/DELETE cannot leave the precondition stale.
// If stored rows are rejected, the diff uses those rows while current_generation
// remains the last distributed generation.
func (s *Server) ApplyImport(ctx context.Context, body []byte, opts ImportOptions) (*ImportResult, error) {
	s.mu.Lock()
	store := s.policyStore
	identityConfig := s.identityConfig // startup-immutable
	s.mu.Unlock()
	if store == nil {
		return nil, ErrNoPolicyStore
	}

	docs, err := policy.ParseWireDocs(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPolicyValidation, err)
	}
	seen := make(map[string]bool, len(docs))
	for i := range docs {
		name := docs[i].Metadata.Name
		if name == "" {
			return nil, fmt.Errorf("%w: document %d: metadata.name is required", ErrPolicyValidation, i+1)
		}
		if seen[name] {
			return nil, fmt.Errorf("%w: duplicate policy name %q", ErrPolicyValidation, name)
		}
		seen[name] = true
	}
	if err := policy.ValidateIdentitySubjects(docs, identityConfig); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPolicyValidation, err)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Metadata.Name < docs[j].Metadata.Name })

	// Canonical bytes: one stored row per document, rendered the same way
	// export.go and AttachPolicyStore render, so the diff is semantic (a
	// comment or key-order change is "unchanged") and export→import is a
	// no-op.
	rows := make([]policystore.Doc, 0, len(docs))
	incoming := make(map[string][]byte, len(docs))
	for i := range docs {
		raw, err := yaml.Marshal(&docs[i])
		if err != nil {
			return nil, fmt.Errorf("%w: render policy %q: %w", ErrPolicyValidation, docs[i].Metadata.Name, err)
		}
		rows = append(rows, policystore.Doc{Name: docs[i].Metadata.Name, YAML: raw})
		incoming[docs[i].Metadata.Name] = raw
	}

	// Predict the installed generation from the SAME bytes ReloadFromStore
	// will parse, so preview and apply can never disagree.
	var reparsed []v1alpha1.GovernancePolicy
	for _, row := range rows {
		d, err := policy.ParseWireDocs(row.YAML)
		if err != nil {
			return nil, fmt.Errorf("%w: canonical policy %q: %w", ErrPolicyValidation, row.Name, err)
		}
		reparsed = append(reparsed, d...)
	}
	resultGen := policy.GenerationOf(reparsed)
	syncBytes, err := worstCaseSyncBytes(reparsed, time.Now())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPolicyValidation, err)
	}
	if syncBytes > importMaxSyncBytes {
		return nil, fmt.Errorf("%w: worst-case sync response %d bytes exceeds the %d-byte data-plane decode bound", ErrPolicyValidation, syncBytes, importMaxSyncBytes)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var storedRows []policystore.Doc
	rejectedStoreSet := false
	if err := s.ReloadFromStore(ctx); err != nil {
		if errors.Is(err, errStoreList) {
			return nil, fmt.Errorf("controlplane: reconcile before import: %w", err)
		}
		// A restore must be able to overwrite rejected stored rows. Diff
		// against stored rows so removals are counted and audited.
		storedRows, err = store.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("controlplane: list rejected stored policies before import: %w", err)
		}
		rejectedStoreSet = true
	}
	s.mu.Lock()
	currentGen := s.generation
	current := append([]v1alpha1.GovernancePolicy(nil), s.wire...)
	s.mu.Unlock()

	existing := make(map[string][]byte, len(current))
	if rejectedStoreSet {
		for _, row := range storedRows {
			existing[row.Name] = row.YAML
			docs, err := policy.ParseWireDocs(row.YAML)
			if err != nil || len(docs) != 1 || docs[0].Metadata.Name != row.Name {
				continue
			}
			raw, err := yaml.Marshal(&docs[0])
			if err != nil {
				return nil, fmt.Errorf("controlplane: render stored policy %q: %w", row.Name, err)
			}
			existing[row.Name] = raw
		}
	} else {
		for i := range current {
			raw, err := yaml.Marshal(&current[i])
			if err != nil {
				return nil, fmt.Errorf("controlplane: render current policy %q: %w", current[i].Metadata.Name, err)
			}
			existing[current[i].Metadata.Name] = raw
		}
	}

	res := &ImportResult{DryRun: opts.DryRun, CurrentGeneration: currentGen, ResultGeneration: resultGen, StoreRejected: rejectedStoreSet}
	for _, row := range rows {
		prev, ok := existing[row.Name]
		switch {
		case !ok:
			res.Changes = append(res.Changes, ImportChange{Name: row.Name, Action: "added"})
			res.Summary.Added++
		case string(prev) == string(row.YAML):
			res.Changes = append(res.Changes, ImportChange{Name: row.Name, Action: "unchanged"})
			res.Summary.Unchanged++
		default:
			res.Changes = append(res.Changes, ImportChange{Name: row.Name, Action: "changed"})
			res.Summary.Changed++
		}
	}
	for name := range existing {
		if _, ok := incoming[name]; !ok {
			res.Changes = append(res.Changes, ImportChange{Name: name, Action: "removed"})
			res.Summary.Removed++
		}
	}
	sort.Slice(res.Changes, func(i, j int) bool { return res.Changes[i].Name < res.Changes[j].Name })
	if res.Changes == nil {
		res.Changes = []ImportChange{}
	}

	if opts.DryRun {
		return res, nil
	}
	if len(docs) == 0 && !opts.AllowEmpty {
		return nil, fmt.Errorf("%w: bundle contains no GovernancePolicy documents; pass allow_empty=true to remove every policy", ErrPolicyValidation)
	}
	if opts.ExpectedGeneration == "" {
		return nil, fmt.Errorf("%w: expected_generation is required to apply an import; run with dry_run=true first", ErrPolicyValidation)
	}
	if opts.ExpectedGeneration != currentGen {
		return nil, ErrImportConflict
	}
	audit := func() {
		for _, c := range res.Changes {
			switch c.Action {
			case "added", "changed":
				s.recordMutation(ctx, "put", c.Name, incoming[c.Name])
			case "removed":
				s.recordMutation(ctx, "delete", c.Name, nil)
			}
		}
		s.recordMutation(ctx, "import", "", append([]byte{}, body...)) // never nil: an empty bundle still hashes
	}
	if err := store.Replace(ctx, rows); err != nil {
		// The outcome may be indeterminate (a commit whose acknowledgement
		// was lost). Read back on a detached, bounded ctx: if the store now
		// holds exactly the predicted set, the restore DID commit.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if rerr := s.ReloadFromStore(rctx); rerr == nil {
			s.mu.Lock()
			committed := s.generation == res.ResultGeneration
			s.mu.Unlock()
			if committed {
				audit()
				return res, nil
			}
		}
		return nil, fmt.Errorf("controlplane: import policies: %w", err)
	}
	// Committed: audit first (a committed restore is never unaudited), then
	// install the validated, reparsed snapshot directly — the exact set
	// ReloadFromStore would produce (store.List is byte-ordered, rows are
	// byte-sorted) — so a post-commit DB read failure or a canceled request
	// cannot leave memory on the replaced set.
	audit()
	committedAt := time.Now()
	if err := s.applyWire(reparsed, nil); err != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if rerr := s.ReloadFromStore(rctx); rerr != nil {
			log.Printf("inferplaned: policy import committed but reload failed: %v", rerr)
		}
		return nil, fmt.Errorf("%w: %w", ErrImportNotApplied, err)
	}
	updated := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		updated[row.Name] = committedAt
	}
	s.mu.Lock()
	s.updated = updated
	res.ResultGeneration = s.generation
	s.mu.Unlock()
	return res, nil
}

// worstCaseSyncBytes measures a superset of the memory and durable-authority
// responses for docs, plus the bounded request-driven authority payload.
func worstCaseSyncBytes(docs []v1alpha1.GovernancePolicy, now time.Time) (int, error) {
	maxDigest := strings.Repeat("f", 64)
	env := policy.SyncResponse{
		IdentityFingerprint:      maxDigest,
		IdentityPoliciesComplete: true,
		Generation:               maxDigest,
		Policies:                 docs,
		SyncIntervalSeconds:      math.MaxInt32,
		Authority: &policy.AuthorityResponse{
			AuthorityID:         maxDigest,
			Protocol:            policy.AuthorityProtocol,
			ServerTime:          now,
			Generation:          maxDigest,
			Policies:            docs,
			IdentityFingerprint: maxDigest,
		},
	}
	for i := range docs {
		p, err := policy.FromV1Alpha1(&docs[i])
		if err != nil {
			return 0, fmt.Errorf("controlplane: measure policy %q: %w", docs[i].Metadata.Name, err)
		}
		for _, r := range p.Rules {
			if r.Budget != nil && !r.Budget.Unlimited {
				// Count routing-only and user-scoped budgets too, even though
				// memory mode does not grant leases for them.
				env.Leases = append(env.Leases, policy.LeaseGrant{
					Policy: p.Name, Rule: r.Name, Team: p.Subject.Team,
					AllowanceMicroUSD: math.MaxInt64,
					ExpiresAt:         now.AddDate(100, 0, 0),
					HardCap:           true,
					Period:            r.Budget.Period,
				})
			}
			if r.Routing == nil || r.Routing.BudgetTiers == nil {
				continue
			}
			bt := r.Routing.BudgetTiers
			var largest policy.BudgetTier
			largestBytes := 0
			for _, tier := range bt.Tiers {
				raw, err := json.Marshal(tier.Substitute)
				if err != nil {
					return 0, fmt.Errorf("controlplane: encode tier substitute: %w", err)
				}
				if len(raw) > largestBytes {
					largest, largestBytes = tier, len(raw)
				}
			}
			env.ActiveTiers = append(env.ActiveTiers, policy.ActiveTier{
				Policy: p.Name, Rule: r.Name, BudgetRef: bt.BudgetRef,
				Team: p.Subject.Team, User: p.Subject.User,
				ThresholdPercent: largest.ThresholdPercent, Substitute: largest.Substitute,
				EnforceTargets: bt.EnforceTargets,
			})
		}
	}
	// Authority mode itself refuses a set whose budgets cannot be derived;
	// that must not prevent a valid memory-mode import.
	if budgets, err := policy.AuthorityBudgets(docs, now); err == nil {
		env.Authority.Budgets = budgets
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return 0, fmt.Errorf("controlplane: encode worst-case sync response: %w", err)
	}

	ack, err := json.Marshal(policy.AuthorityAck{
		Instance: maxDigest, ID: maxDigest, Sequence: math.MaxInt64,
	})
	if err != nil {
		return 0, fmt.Errorf("controlplane: encode authority ack: %w", err)
	}
	maxItem := len(ack)
	if len(env.Authority.Budgets) != 0 {
		var largest policy.AuthorityBudget
		largestBytes := 0
		for _, b := range env.Authority.Budgets {
			identifiers, err := json.Marshal([]string{b.Key, b.Revision, b.WindowID})
			if err != nil {
				return 0, fmt.Errorf("controlplane: encode authority budget identifiers: %w", err)
			}
			if len(identifiers) > largestBytes {
				largest, largestBytes = b, len(identifiers)
			}
		}
		grant, err := json.Marshal(policy.AuthorityGrant{
			ID: maxDigest, Key: largest.Key, Revision: largest.Revision, WindowID: largest.WindowID,
			Amount: math.MaxInt64, ExpiresAt: now,
		})
		if err != nil {
			return 0, fmt.Errorf("controlplane: encode authority grant: %w", err)
		}
		denial, err := json.Marshal(policy.AuthorityDenial{
			Key: largest.Key, Reason: strings.Repeat("x", 512),
		})
		if err != nil {
			return 0, fmt.Errorf("controlplane: encode authority denial: %w", err)
		}
		maxItem = max(maxItem, len(grant), len(denial))
	}
	const (
		authorityItemCap     = 1024
		syncRequestEchoBytes = 1 << 20
	)
	requestDriven := authorityItemCap*maxItem + syncRequestEchoBytes
	return len(raw) + requestDriven, nil
}

// mountImport registers POST /v1alpha1/config/import behind the policy
// WRITE authority (authnWrite): the heartbeat token is never accepted.
func (s *Server) mountImport(mux *http.ServeMux) {
	s.mu.Lock()
	writeToken := s.writeToken
	s.mu.Unlock()
	mux.HandleFunc("POST /v1alpha1/config/import", authnWrite(s.token, writeToken, s.authOpts, s.handleConfigImport))
}

func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, importMaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "policy bundle too large")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "could not read request body")
		return
	}
	q := r.URL.Query()
	res, err := s.ApplyImport(r.Context(), body, ImportOptions{
		DryRun:             q.Get("dry_run") == "true",
		ExpectedGeneration: q.Get("expected_generation"),
		AllowEmpty:         q.Get("allow_empty") == "true",
	})
	if errors.Is(err, ErrImportConflict) {
		s.mu.Lock()
		gen := s.generation
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": ErrImportConflict.Error(), "current_generation": gen})
		return
	}
	if errors.Is(err, ErrImportNotApplied) {
		writeJSONError(w, http.StatusInternalServerError, "policies were committed but not applied; re-run a dry run to confirm the distributed set")
		return
	}
	if err != nil {
		writePolicyError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(res)
}
