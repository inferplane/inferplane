package keystore

import (
	"context"
	"database/sql"
	"slices"

	"github.com/inferplane/inferplane/internal/identity"
)

type identityExport struct {
	Config   identity.Config
	Bindings []identity.Binding
}

func readSQLiteIdentity(ctx context.Context, tx *sql.Tx) (identityExport, error) {
	var out identityExport
	var tables int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('identity_mode','identity_registry')`).Scan(&tables)
	if err != nil {
		return out, identityStorageError(err)
	}
	if tables == 0 {
		return out, nil // supported pre-identity source schema
	}
	if tables != 2 {
		return out, ErrSnapshotChanged
	}
	itx := identityTx{ctx: ctx, sql: tx}
	mode, err := itx.mode()
	if err != nil {
		return out, err
	}
	out.Config, err = mode.config()
	if err != nil {
		return out, err
	}
	rows, closeRows, err := itx.rows(`SELECT ` + identityBindingColumns + ` FROM identity_registry ORDER BY organization,kind,issuer,subject`)
	if err != nil {
		return out, err
	}
	defer closeRows()
	for rows.Next() {
		b, found, err := scanIdentityBinding(rows)
		if err != nil || !found || b.Identity.Organization != out.Config.Organization {
			return out, ErrSnapshotChanged
		}
		if b.AccountRef != b.Identity.CanonicalRef() && !slices.Contains(out.Config.Bindings, b) {
			return out, ErrSnapshotChanged
		}
		out.Bindings = append(out.Bindings, b)
	}
	if err := rows.Err(); err != nil {
		return out, identityStorageError(err)
	}
	for _, declared := range out.Config.Bindings {
		found := false
		for _, b := range out.Bindings {
			found = found || b == declared
		}
		if !found {
			return out, ErrSnapshotChanged
		}
	}
	return out, nil
}

func (t identityTx) stageIdentityImport(source identityExport) error {
	m, err := t.mode()
	if err != nil {
		return err
	}
	if m.Organization != "" && m.Fingerprint != source.Config.Fingerprint() {
		return ErrSnapshotChanged
	}
	if source.Config.Organization != "" && m.Organization == "" {
		staged := source.Config
		staged.Required = false
		if err := t.putMode(staged); err != nil {
			return err
		}
	}
	for _, b := range source.Bindings {
		if err := t.bind(b); err != nil {
			return err
		}
	}
	return nil
}

func (t identityTx) finishIdentityImport(source identityExport) error {
	if source.Config.Required {
		if err := t.validateRequiredIdentities(); err != nil {
			return err
		}
	}
	if source.Config.Organization != "" {
		return t.putMode(source.Config)
	}
	return nil
}
