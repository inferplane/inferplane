// Package policystore is inferplaned's optional DB-authoritative store for
// GovernancePolicy documents (ADR-038). It persists each document's bytes
// verbatim and never parses them — validation belongs to internal/policy and
// is applied by the controlplane layer — which keeps this package a leaf
// whose only non-stdlib imports are pgx/v5 and pgx/v5/pgxpool.
package policystore

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Delete when no document of that name exists.
var ErrNotFound = errors.New("policystore: not found")

// Doc is one stored GovernancePolicy document. YAML holds the document bytes
// VERBATIM as written (YAML or JSON — YAML is a superset); this package never
// parses them.
type Doc struct {
	Name      string
	YAML      []byte
	UpdatedAt time.Time
}

// Store persists GovernancePolicy documents whole — the document, not the
// individual rule, is the CRUD unit (budget/rate/modelAccess share one doc).
type Store interface {
	// List returns every document ordered by name (deterministic order is
	// load-bearing: policy.GenerationOf is order-sensitive).
	List(ctx context.Context) ([]Doc, error)
	// Put upserts one whole document.
	Put(ctx context.Context, name string, docYAML []byte) error
	// Delete removes one document, ErrNotFound when absent.
	Delete(ctx context.Context, name string) error
	// Seeded reports whether the one-time file→DB seed has run.
	Seeded(ctx context.Context) (bool, error)
	// Seed imports docs AND sets the durable marker in ONE transaction, but
	// only if not already seeded. Returns true if it seeded. The MARKER, not a
	// row count, gates this — an emptied store is never re-seeded
	// (internal/providerstore precedent).
	Seed(ctx context.Context, docs []Doc) (bool, error)
}
