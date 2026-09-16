package keystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/inferplane/inferplane/internal/identity"
	"github.com/jackc/pgx/v5"
)

type identityHandle struct{ fingerprint atomic.Pointer[string] }

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (h *identityHandle) configured(m identityMode) error {
	if m.Required != 0 {
		fp := h.fingerprint.Load()
		if fp == nil || *fp != m.Fingerprint {
			return ErrStoreUnavailable
		}
	}
	return nil
}

func (h *identityHandle) remember(c identity.Config) {
	fp := c.Fingerprint()
	h.fingerprint.Store(&fp)
}

type identityMode struct {
	Organization string `json:"organization"`
	Required     int    `json:"required"`
	Fingerprint  string `json:"fingerprint"`
	Declaration  string `json:"declaration"`
}

func (m identityMode) config() (identity.Config, error) {
	if m.Organization == "" {
		if m.Required != 0 || m.Fingerprint != "" || m.Declaration != "" {
			return identity.Config{}, ErrStoreUnavailable
		}
		return identity.Config{}, nil
	}
	var c identity.Config
	if json.Unmarshal([]byte(m.Declaration), &c) != nil || c.Validate() != nil ||
		c.Organization != m.Organization || (m.Required != 0 && m.Required != 1) ||
		c.Required != (m.Required == 1) || c.Fingerprint() != m.Fingerprint {
		return identity.Config{}, ErrStoreUnavailable
	}
	return c, nil
}

type identityRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

type identitySQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// A small private adapter keeps the registry state machine identical on both
// backends. All statements are package-owned SQL, never caller-supplied SQL.
type identityTx struct {
	ctx context.Context
	sql identitySQL
	pg  pgx.Tx
}

// Internal statements use PostgreSQL $N numbering. The modernc SQLite adapter
// maps that to SQLite's indexed ?NNN parameters, preserving repeated/out-of-order
// references; replacing them with bare ? would bind different arguments.
// Only fixed internal SQL reaches this adapter; values remain bound parameters.
var identityPlaceholder = regexp.MustCompile(`\$(\d+)`)

func (t identityTx) exec(query string, args ...any) error {
	var err error
	if t.pg != nil {
		_, err = t.pg.Exec(t.ctx, query, args...)
	} else {
		_, err = t.sql.ExecContext(t.ctx, identityPlaceholder.ReplaceAllString(query, "?$1"), args...)
	}
	return identityStorageError(err)
}

func (t identityTx) row(query string, args ...any) interface{ Scan(...any) error } {
	if t.pg != nil {
		return t.pg.QueryRow(t.ctx, query, args...)
	}
	return t.sql.QueryRowContext(t.ctx, identityPlaceholder.ReplaceAllString(query, "?$1"), args...)
}

func (t identityTx) rows(query string, args ...any) (identityRows, func(), error) {
	if t.pg != nil {
		rows, err := t.pg.Query(t.ctx, query, args...)
		if err != nil {
			return nil, nil, identityStorageError(err)
		}
		return rows, rows.Close, nil
	}
	rows, err := t.sql.QueryContext(t.ctx, identityPlaceholder.ReplaceAllString(query, "?$1"), args...)
	if err != nil {
		return nil, nil, identityStorageError(err)
	}
	return rows, func() { _ = rows.Close() }, nil
}

func identityStorageError(err error) error {
	if err == nil {
		return nil
	}
	// Driver errors can contain a tuple, reference, SQL parameters or DSN.
	return fmt.Errorf("keystore identity storage: %w", ErrStoreUnavailable)
}

func (t identityTx) mode() (identityMode, error) {
	var m identityMode
	err := t.row(`SELECT organization,required,fingerprint,declaration FROM identity_mode WHERE singleton=1`).
		Scan(&m.Organization, &m.Required, &m.Fingerprint, &m.Declaration)
	if err != nil {
		return m, identityStorageError(err)
	}
	_, err = m.config()
	return m, err
}

func (t identityTx) putMode(c identity.Config) error {
	c.Bindings = slices.Clone(c.Bindings)
	slices.SortFunc(c.Bindings, func(a, b identity.Binding) int {
		return strings.Compare(a.Identity.CanonicalRef(), b.Identity.CanonicalRef())
	})
	raw, err := json.Marshal(c)
	if err != nil {
		return identityStorageError(err)
	}
	required := 0
	if c.Required {
		required = 1
	}
	return t.exec(`UPDATE identity_mode SET organization=$1,required=$2,fingerprint=$3,declaration=$4 WHERE singleton=1`,
		c.Organization, required, c.Fingerprint(), string(raw))
}

func scanIdentityBinding(row interface{ Scan(...any) error }) (identity.Binding, bool, error) {
	var b identity.Binding
	err := row.Scan(&b.Identity.Organization, &b.Identity.Kind, &b.Identity.Issuer, &b.Identity.Subject, &b.AccountRef)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return b, false, nil
	}
	if err != nil {
		return b, false, identityStorageError(err)
	}
	if b.Validate() != nil {
		return b, false, ErrStoreUnavailable
	}
	return b, true, nil
}

const identityBindingColumns = `organization,kind,issuer,subject,account_ref`

func (t identityTx) lookup(id identity.ID) (identity.Binding, bool, error) {
	return scanIdentityBinding(t.row(`SELECT `+identityBindingColumns+` FROM identity_registry
		WHERE organization=$1 AND kind=$2 AND issuer=$3 AND subject=$4`,
		id.Organization, string(id.Kind), id.Issuer, id.Subject))
}

func (t identityTx) bind(b identity.Binding) error {
	if b.Validate() != nil {
		return identity.ErrInvalid
	}
	if err := t.exec(`INSERT INTO identity_registry(organization,kind,issuer,subject,account_ref)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		b.Identity.Organization, string(b.Identity.Kind), b.Identity.Issuer, b.Identity.Subject, b.AccountRef); err != nil {
		return err
	}
	existing, found, err := t.lookup(b.Identity)
	if err != nil {
		return err
	}
	if !found || existing != b {
		return ErrSnapshotChanged
	}
	return nil
}

func identityFields(id *identity.ID) (org, kind, issuer, subject string) {
	if id != nil {
		return id.Organization, string(id.Kind), id.Issuer, id.Subject
	}
	return "", "", "", ""
}

func identityFromFields(org, kind, issuer, subject string) (*identity.ID, error) {
	if org == "" && kind == "" && issuer == "" && subject == "" {
		return nil, nil
	}
	id := identity.ID{Organization: org, Kind: identity.Kind(kind), Issuer: issuer, Subject: subject}
	if id.Validate() != nil {
		return nil, ErrStoreUnavailable
	}
	return &id, nil
}

func (t identityTx) prepareOptions(opts KeyOptions, m identityMode) (KeyOptions, error) {
	if opts.Identity == nil {
		if m.Required != 0 {
			return opts, ErrSnapshotChanged
		}
		return opts, nil
	}
	id := *opts.Identity
	if id.Validate() != nil || id.Organization != m.Organization {
		return opts, ErrSnapshotChanged
	}
	opts.Identity = &id
	if m.Required == 0 {
		return opts, nil // typed attribution is dormant; Owner remains exact.
	}
	b, found, err := t.lookup(id)
	if err != nil {
		return opts, err
	}
	if !found {
		b = identity.Binding{Identity: id, AccountRef: id.CanonicalRef()}
		if err := t.bind(b); err != nil {
			return opts, err
		}
	}
	if opts.Owner != "" && opts.Owner != b.AccountRef {
		return opts, ErrSnapshotChanged
	}
	opts.Owner = b.AccountRef
	return opts, nil
}

func (t identityTx) preserveKeyIdentity(hash string, opts KeyOptions, m identityMode) (KeyOptions, error) {
	var org, kind, issuer, subject string
	err := t.row(`SELECT identity_organization,identity_kind,identity_issuer,identity_subject FROM keys WHERE key_hash=$1`, hash).
		Scan(&org, &kind, &issuer, &subject)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return opts, nil
	}
	if err != nil {
		return opts, identityStorageError(err)
	}
	existing, err := identityFromFields(org, kind, issuer, subject)
	if err != nil {
		return opts, err
	}
	if existing != nil {
		if opts.Identity == nil && m.Required == 0 {
			opts.Identity = existing
		} else if opts.Identity != nil && *opts.Identity != *existing {
			return opts, ErrSnapshotChanged
		}
	}
	return opts, nil
}

// Safe upserts and seed retries present the stored tombstone to INSERT guards.
// They never ask the database to reactivate it or move its ID/hash. The caller
// holds the key write lock and invokes this only in required identity mode.
func (t identityTx) preserveKeyTombstone(k *postgresKey) error {
	var id string
	var revoked int
	err := t.row(`SELECT key_id,revoked FROM keys WHERE key_hash=$1`, k.Hash).Scan(&id, &revoked)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return identityStorageError(err)
	}
	if revoked != 0 {
		k.KeyID, k.Revoked = id, revoked
	}
	return nil
}

func (t identityTx) annotate(b identity.Binding) error {
	var conflict int
	if err := t.row(`SELECT COUNT(*) FROM keys WHERE owner=$1 AND
		NOT (identity_organization='' AND identity_kind='' AND identity_issuer='' AND identity_subject='') AND
		NOT (identity_organization=$2 AND identity_kind=$3 AND identity_issuer=$4 AND identity_subject=$5)`,
		b.AccountRef, b.Identity.Organization, string(b.Identity.Kind), b.Identity.Issuer, b.Identity.Subject).Scan(&conflict); err != nil {
		return identityStorageError(err)
	}
	if conflict != 0 {
		return ErrSnapshotChanged
	}
	return t.exec(`UPDATE keys SET identity_organization=$2,identity_kind=$3,identity_issuer=$4,identity_subject=$5
		WHERE owner=$1 AND identity_organization='' AND identity_kind='' AND identity_issuer='' AND identity_subject=''`,
		b.AccountRef, b.Identity.Organization, string(b.Identity.Kind), b.Identity.Issuer, b.Identity.Subject)
}

func (t identityTx) validateRequiredIdentities() error {
	var missing int
	err := t.row(`SELECT COUNT(*) FROM keys k LEFT JOIN identity_registry i ON
		i.organization=k.identity_organization AND i.kind=k.identity_kind AND
		i.issuer=k.identity_issuer AND i.subject=k.identity_subject AND i.account_ref=k.owner
		WHERE (k.revoked=0 OR k.owner<>'') AND i.account_ref IS NULL`).Scan(&missing)
	if err != nil {
		return identityStorageError(err)
	}
	if missing != 0 {
		return fmt.Errorf("%w: %w", ErrSnapshotChanged, ErrIdentityNeedsMapping)
	}
	return nil
}

func (t identityTx) configure(c identity.Config) (identity.Config, error) {
	if err := c.Validate(); err != nil {
		return identity.Config{}, err
	}
	m, err := t.mode()
	if err != nil {
		return identity.Config{}, err
	}
	old, _ := m.config()
	if c.Organization == "" {
		if old.Required {
			return identity.Config{}, ErrSnapshotChanged
		}
		return old, nil // missing optional configuration never clears the registry.
	}
	if old.Organization != "" && old.Organization != c.Organization ||
		old.Required && old.Fingerprint() != c.Fingerprint() {
		return identity.Config{}, ErrSnapshotChanged
	}
	for _, b := range old.Bindings {
		if !slices.Contains(c.Bindings, b) {
			return identity.Config{}, ErrSnapshotChanged
		}
	}
	if !old.Required {
		staged := c
		staged.Required = false
		if err := t.putMode(staged); err != nil {
			return identity.Config{}, err
		}
	}
	for _, b := range c.Bindings {
		if err := t.bind(b); err != nil {
			return identity.Config{}, err
		}
		if !old.Required {
			if err := t.annotate(b); err != nil {
				return identity.Config{}, err
			}
		}
	}
	if c.Required {
		if err := t.validateRequiredIdentities(); err != nil {
			return identity.Config{}, err
		}
	}
	if err := t.putMode(c); err != nil {
		return identity.Config{}, err
	}
	return c, nil
}

func (t identityTx) validatePrincipal(p *Principal, m identityMode) error {
	p.IdentityRequired, p.IdentityFingerprint = m.Required == 1, m.Fingerprint
	if p.Identity != nil && (p.Identity.Validate() != nil || p.Identity.Organization != m.Organization) {
		return ErrStoreUnavailable
	}
	if m.Required == 0 {
		return nil
	}
	if p.Identity == nil {
		return ErrStoreUnavailable
	}
	b, found, err := t.lookup(*p.Identity)
	if err != nil {
		return err
	}
	if !found || p.Owner != b.AccountRef {
		return ErrStoreUnavailable
	}
	return nil
}

func (s *SQLiteStore) identityTransaction(ctx context.Context, write bool, fn func(identityTx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return identityStorageError(err)
	}
	defer conn.Close()
	begin := "BEGIN"
	if write {
		begin = "BEGIN IMMEDIATE"
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		return identityStorageError(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if err := fn(identityTx{ctx: ctx, sql: conn}); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return identityStorageError(err)
}

func (s *SQLiteStore) ConfigureIdentity(ctx context.Context, c identity.Config) error {
	var applied identity.Config
	err := s.identityTransaction(ctx, true, func(tx identityTx) error {
		var err error
		applied, err = tx.configure(c)
		return err
	})
	if err == nil {
		s.identityHandle.remember(applied)
	}
	return err
}

func (s *SQLiteStore) IdentityConfig(ctx context.Context) (identity.Config, error) {
	var c identity.Config
	err := s.identityTransaction(ctx, false, func(tx identityTx) error {
		m, err := tx.mode()
		if err == nil {
			c, err = m.config()
		}
		return err
	})
	return c, err
}

func (s *SQLiteStore) ResolveIdentity(ctx context.Context, id identity.ID) (identity.Binding, bool, error) {
	var b identity.Binding
	var found bool
	err := s.identityTransaction(ctx, false, func(tx identityTx) error {
		if id.Validate() != nil {
			return identity.ErrInvalid
		}
		var err error
		b, found, err = tx.lookup(id)
		return err
	})
	return b, found, err
}

func (s *SQLiteStore) IdentityByRef(ctx context.Context, ref string) (identity.Binding, bool, error) {
	var b identity.Binding
	var found bool
	err := s.identityTransaction(ctx, false, func(tx identityTx) error {
		var err error
		b, found, err = scanIdentityBinding(tx.row(`SELECT `+identityBindingColumns+` FROM identity_registry WHERE account_ref=$1`, ref))
		return err
	})
	return b, found, err
}

func lockIdentityFinancialTables(ctx context.Context, tx pgx.Tx) error {
	for _, table := range []string{"authority_requests", "shared_permits"} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			return identityStorageError(err)
		}
		if exists {
			if _, err := tx.Exec(ctx, `LOCK TABLE `+table+` IN EXCLUSIVE MODE`); err != nil {
				return identityStorageError(err)
			}
		}
	}
	return nil
}

func lockIdentityMode(ctx context.Context, tx pgx.Tx) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('identity_mode') IS NOT NULL`).Scan(&exists); err != nil {
		return identityStorageError(err)
	}
	if exists {
		_, err := tx.Exec(ctx, `LOCK TABLE identity_mode IN SHARE ROW EXCLUSIVE MODE`)
		return identityStorageError(err)
	}
	return nil
}

func (s *PostgresStore) ConfigureIdentity(ctx context.Context, c identity.Config) error {
	ctx, cancel := context.WithTimeout(ctx, postgresTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return identityStorageError(err)
	}
	defer rollbackPostgres(tx)
	if err := lockIdentityMode(ctx, tx); err != nil {
		return err
	}
	if err := lockPostgresIdentity(ctx, tx); err != nil {
		return err
	}
	if err := lockIdentityFinancialTables(ctx, tx); err != nil {
		return err
	}
	applied, err := (identityTx{ctx: ctx, pg: tx}).configure(c)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return identityStorageError(err)
	}
	s.identityHandle.remember(applied)
	return nil
}

func (s *PostgresStore) IdentityConfig(ctx context.Context) (identity.Config, error) {
	ctx, cancel := context.WithTimeout(ctx, postgresTimeout)
	defer cancel()
	var m identityMode
	err := s.db.QueryRow(ctx, `SELECT organization,required,fingerprint,declaration FROM identity_mode WHERE singleton=1`).
		Scan(&m.Organization, &m.Required, &m.Fingerprint, &m.Declaration)
	if err != nil {
		return identity.Config{}, identityStorageError(err)
	}
	return m.config()
}

func (s *PostgresStore) ResolveIdentity(ctx context.Context, id identity.ID) (identity.Binding, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, postgresTimeout)
	defer cancel()
	if id.Validate() != nil {
		return identity.Binding{}, false, identity.ErrInvalid
	}
	return scanIdentityBinding(s.db.QueryRow(ctx, `SELECT `+identityBindingColumns+` FROM identity_registry
		WHERE organization=$1 AND kind=$2 AND issuer=$3 AND subject=$4`,
		id.Organization, string(id.Kind), id.Issuer, id.Subject))
}

func (s *PostgresStore) IdentityByRef(ctx context.Context, ref string) (identity.Binding, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, postgresTimeout)
	defer cancel()
	return scanIdentityBinding(s.db.QueryRow(ctx, `SELECT `+identityBindingColumns+` FROM identity_registry WHERE account_ref=$1`, ref))
}

var (
	_ IdentityStore = (*SQLiteStore)(nil)
	_ IdentityStore = (*PostgresStore)(nil)
)
