package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
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

// Size contract. The FLEET bound is semantic: a data plane decodes a sync
// response of at most 8 MiB of JSON (internal/proxy/sync.go: 8<<20), so an
// import whose parsed set exceeds importMaxSetJSONBytes as JSON is refused —
// it could never be distributed. The TRANSPORT bound is only a memory cap:
// block YAML adds per-line indentation that JSON lacks (policy nesting is
// schema-fixed, so the overhead is bounded; a multiline-string fixture
// measured ~4.9x, and newline-per-byte strings at maximum depth stay under
// ~7x), so 64 MiB of YAML admits the export of every set within the fleet
// bound. policyMaxBodyBytes (1 MiB) stays the per-document PUT bound.
const importMaxBodyBytes = 64 << 20

// importMaxSetJSONBytes is a var only so tests can lower it.
var importMaxSetJSONBytes = 8 << 20

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
}

// ApplyImport makes body (multi-document GovernancePolicy YAML, the exact
// GET /v1alpha1/config/export format) the COMPLETE policy set — backup
// restore, not merge. Unlike ApplyWrite, the whole bundle is validated
// BEFORE the store is touched: ParseWireDocs runs every document through
// policy.FromV1Alpha1, and names/duplicates/identity subjects are checked
// here, which is every way applyWire can reject a set.
//
// Both preview and apply hold writeMu and first reconcile memory with the
// store, so the previewed current_generation and the apply precondition are
// judged against what the store actually holds — never against memory left
// stale by a committed-but-unreloaded PUT/DELETE or an unacknowledged commit.
func (s *Server) ApplyImport(ctx context.Context, body []byte, opts ImportOptions) (*ImportResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	store := s.policyStore
	s.mu.Unlock()
	if store == nil {
		return nil, ErrNoPolicyStore
	}
	if err := s.ReloadFromStore(ctx); err != nil && errors.Is(err, errStoreList) {
		return nil, fmt.Errorf("controlplane: reconcile before import: %w", err)
	}
	// (A rejected stored set — out-of-band DB edit — falls through: memory
	// still holds the last set actually distributed, and the restore is
	// precisely the tool to overwrite those rows.)
	s.mu.Lock()
	identityConfig := s.identityConfig
	currentGen := s.generation
	current := append([]v1alpha1.GovernancePolicy(nil), s.wire...)
	s.mu.Unlock()

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
	existing := make(map[string][]byte, len(current))
	for i := range current {
		raw, err := yaml.Marshal(&current[i])
		if err != nil {
			return nil, fmt.Errorf("controlplane: render current policy %q: %w", current[i].Metadata.Name, err)
		}
		existing[current[i].Metadata.Name] = raw
	}

	res := &ImportResult{DryRun: opts.DryRun, CurrentGeneration: currentGen}
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
	res.ResultGeneration = policy.GenerationOf(reparsed)
	if setJSON, err := json.Marshal(reparsed); err != nil || len(setJSON) > importMaxSetJSONBytes {
		return nil, fmt.Errorf("%w: the policy set exceeds the %d-byte JSON bound a data plane can receive", ErrPolicyValidation, importMaxSetJSONBytes)
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
