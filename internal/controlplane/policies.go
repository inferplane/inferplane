// Policy store integration + write API (ADR-038): when a policystore.Store
// is attached, the database becomes the authoritative source of the
// GovernancePolicy document set — the --policies file channel is the
// one-time seed source — and PUT/DELETE /v1alpha1/policies/{name} persist a
// document and hot-apply it through the same applyWire path the file loader
// uses. With no store attached, GET still serves the in-memory set
// (writable:false) and the write verbs answer 405.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/inferplane/inferplane/internal/adminauth"
	"io"
	"net/http"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/policystore"
	"sigs.k8s.io/yaml"
)

// ErrNoPolicyStore is returned by the write paths when no policy store is
// attached — the file channel is read-only by construction. Surfaces as 405,
// the console's capability signal.
var ErrNoPolicyStore = errors.New("controlplane: no policy store configured")

// ErrPolicyValidation wraps every caller-fixable rejection of a submitted
// document (parse failure, wrong document count, name mismatch). Its message
// IS shown to the caller — policies carry rules and secret REFS only, never
// secret values, so echoing the validator's reason leaks nothing.
var ErrPolicyValidation = errors.New("invalid policy document")

// policyMaxBodyBytes bounds a submitted document (the 1 MiB sync bound).
const policyMaxBodyBytes = 1 << 20

// AttachPolicyStore makes store the authoritative source of policy documents.
// If the store has never been seeded, the CURRENT (file-loaded) document set
// is imported into it first — the internal/providerstore file=seed/DB=truth
// hand-off, marker-gated, so a store whose documents were all deleted is
// never re-seeded from the image's files. Boot-time; call before Mount.
func (s *Server) AttachPolicyStore(ctx context.Context, store policystore.Store) error {
	seeded, err := store.Seeded(ctx)
	if err != nil {
		return fmt.Errorf("controlplane: policy store seed check: %w", err)
	}
	if !seeded {
		s.mu.Lock()
		wire := append([]v1alpha1.GovernancePolicy(nil), s.wire...)
		s.mu.Unlock()
		docs := make([]policystore.Doc, 0, len(wire))
		for i := range wire {
			raw, err := yaml.Marshal(&wire[i]) // sigs.k8s.io/yaml, as export.go
			if err != nil {
				return fmt.Errorf("controlplane: render policy %q for seeding: %w", wire[i].Metadata.Name, err)
			}
			docs = append(docs, policystore.Doc{Name: wire[i].Metadata.Name, YAML: raw})
		}
		if _, err := store.Seed(ctx, docs); err != nil {
			return fmt.Errorf("controlplane: seed policy store: %w", err)
		}
	}
	s.mu.Lock()
	s.policyStore = store
	s.mu.Unlock()
	return s.ReloadFromStore(ctx)
}

// PolicyStoreAttached reports whether a policy store is authoritative. main
// reads it to skip the file mtime watch.
func (s *Server) PolicyStoreAttached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policyStore != nil
}

// ReloadFromStore replaces the in-memory document set with the store's.
func (s *Server) ReloadFromStore(ctx context.Context) error {
	s.mu.Lock()
	store := s.policyStore
	s.mu.Unlock()
	if store == nil {
		return ErrNoPolicyStore
	}
	rows, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("controlplane: list stored policies: %w", err)
	}
	wire := make([]v1alpha1.GovernancePolicy, 0, len(rows))
	updated := make(map[string]time.Time, len(rows))
	seen := make(map[string]string, len(rows))
	for _, row := range rows {
		docs, err := policy.ParseWireDocs(row.YAML)
		if err != nil {
			return fmt.Errorf("controlplane: stored policy %q: %w", row.Name, err)
		}
		for i := range docs {
			d := &docs[i]
			if d.Metadata.Name == "" {
				return fmt.Errorf("controlplane: stored policy %q: metadata.name is required", row.Name)
			}
			if prev, dup := seen[d.Metadata.Name]; dup {
				return fmt.Errorf("controlplane: stored policy %q: duplicate policy name %q (already defined by %s)", row.Name, d.Metadata.Name, prev)
			}
			seen[d.Metadata.Name] = row.Name
			wire = append(wire, *d)
		}
		updated[row.Name] = row.UpdatedAt
	}
	if err := s.applyWire(wire, nil); err != nil {
		return err
	}
	s.mu.Lock()
	s.updated = updated
	s.mu.Unlock()
	return nil
}

// ApplyWrite persists ONE GovernancePolicy document and hot-applies it.
// Order is deliberate: Postgres commit FIRST, memory second (ADR-036's
// DurableAggregator ordering) — a failed Put leaves the enforced set exactly
// as it was, and there is no window where memory enforces a rule the store
// does not hold.
func (s *Server) ApplyWrite(ctx context.Context, name string, body []byte) error {
	s.mu.Lock()
	store := s.policyStore
	s.mu.Unlock()
	if store == nil {
		return ErrNoPolicyStore
	}
	if name == "" {
		return fmt.Errorf("%w: policy name is required", ErrPolicyValidation)
	}
	docs, err := policy.ParseWireDocs(body)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPolicyValidation, err)
	}
	if len(docs) != 1 {
		return fmt.Errorf("%w: exactly one GovernancePolicy document is required, got %d", ErrPolicyValidation, len(docs))
	}
	if docs[0].Metadata.Name != name {
		return fmt.Errorf("%w: metadata.name %q does not match the URL policy name %q", ErrPolicyValidation, docs[0].Metadata.Name, name)
	}
	// The body VERBATIM, so what an operator submitted is exactly what is
	// stored.
	if err := store.Put(ctx, name, body); err != nil {
		return fmt.Errorf("controlplane: put policy %q: %w", name, err)
	}
	return s.ReloadFromStore(ctx)
}

// ApplyDelete removes one document and hot-applies the reduced set.
func (s *Server) ApplyDelete(ctx context.Context, name string) error {
	s.mu.Lock()
	store := s.policyStore
	s.mu.Unlock()
	if store == nil {
		return ErrNoPolicyStore
	}
	if err := store.Delete(ctx, name); err != nil {
		// %w is required: writePolicyError maps policystore.ErrNotFound to 404
		// via errors.Is.
		return fmt.Errorf("controlplane: delete policy %q: %w", name, err)
	}
	return s.ReloadFromStore(ctx)
}

func (s *Server) mountPolicies(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1alpha1/policies", authn(s.token, s.authOpts, s.handlePolicyList))
	// Writes require policy-admin when role gating is configured
	// (Phase 0b-4); the static token stays platform-admin break-glass.
	mux.HandleFunc("PUT /v1alpha1/policies/{name}", authnCap(s.token, s.authOpts, []string{adminauth.RolePolicyAdmin}, s.handlePolicyPut))
	mux.HandleFunc("DELETE /v1alpha1/policies/{name}", authnCap(s.token, s.authOpts, []string{adminauth.RolePolicyAdmin}, s.handlePolicyDelete))
}

// policyView is one document as the console consumes it: the wire document
// itself (so a client can round-trip it whole, including rules the console
// does not understand) plus its store metadata.
type policyView struct {
	Name      string                    `json:"name"`
	Policy    v1alpha1.GovernancePolicy `json:"policy"`
	UpdatedAt *time.Time                `json:"updated_at,omitempty"`
}

func (s *Server) handlePolicyList(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	wire := append([]v1alpha1.GovernancePolicy(nil), s.wire...)
	generation := s.generation
	updated := s.updated
	writable := s.policyStore != nil
	s.mu.Unlock()

	views := make([]policyView, 0, len(wire))
	for i := range wire {
		v := policyView{Name: wire[i].Metadata.Name, Policy: wire[i]}
		if t, ok := updated[wire[i].Metadata.Name]; ok {
			ts := t // local copy: never take the address of the range variable
			v.UpdatedAt = &ts
		}
		views = append(views, v)
	}
	body := struct {
		Generation string       `json:"generation"`
		Writable   bool         `json:"writable"`
		Policies   []policyView `json:"policies"`
	}{Generation: generation, Writable: writable, Policies: views}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&body)
}

func (s *Server) handlePolicyPut(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, policyMaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "policy document too large")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "could not read request body")
		return
	}
	before := s.canonicalPolicyJSON(name)
	if err := s.ApplyWrite(r.Context(), name, body); err != nil {
		writePolicyError(w, err)
		return
	}
	s.mu.Lock()
	generation := s.generation
	s.mu.Unlock()
	s.recordMutation(MutationRecord{
		Actor: Actor(r.Context()), Capability: "policies", Action: "put", Scope: name,
		BeforeSHA256: sha256Hex(before), AfterSHA256: sha256Hex(s.canonicalPolicyJSON(name)),
		Generation: generation,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePolicyDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	before := s.canonicalPolicyJSON(name)
	if err := s.ApplyDelete(r.Context(), name); err != nil {
		writePolicyError(w, err)
		return
	}
	s.mu.Lock()
	generation := s.generation
	s.mu.Unlock()
	s.recordMutation(MutationRecord{
		Actor: Actor(r.Context()), Capability: "policies", Action: "delete", Scope: name,
		BeforeSHA256: sha256Hex(before),
		Generation:   generation,
	})
	w.WriteHeader(http.StatusNoContent)
}

// writeJSONError encodes the error body. json.Marshal — NOT a formatted JSON
// literal: a validation message legitimately contains quotes and would
// otherwise emit malformed JSON the console cannot parse.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writePolicyError maps a store/validation error to its status. Order
// matters: the sentinels are checked before the catch-all.
func writePolicyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoPolicyStore):
		writeJSONError(w, http.StatusMethodNotAllowed,
			"policy store not configured — set INFERPLANED_POLICY_DSN to edit policies (today: edit the --policies files and redeploy)")
	case errors.Is(err, ErrPolicyValidation):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, policystore.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "no such policy")
	default:
		// Same posture as usage.go: a store failure is "not stored" — 503,
		// with a fixed message. The underlying pgx text is deliberately not
		// echoed (defense in depth on DSN hygiene).
		writeJSONError(w, http.StatusServiceUnavailable, "policy store unavailable")
	}
}
