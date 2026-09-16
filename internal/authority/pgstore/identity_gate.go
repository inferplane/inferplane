package pgstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inferplane/inferplane/internal/identity"
	"github.com/jackc/pgx/v5"
)

// InstallIdentityWriteGuards installs admission-only guards before key-store
// activation. It does not modify financial rows or require provider credentials.
func InstallIdentityWriteGuards(ctx context.Context, dsn string) error {
	s, err := New(dsn)
	if err != nil {
		return err
	}
	defer s.Close()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return databaseError("begin identity guards", err)
	}
	defer rollback(tx)
	if err := installIdentityWriteGuards(ctx, tx); err != nil {
		return err
	}
	return databaseError("commit identity guards", tx.Commit(ctx))
}

// CheckIdentityConfiguration refuses a downgrade of an existing required
// authority without modifying its schema or accounts.
func CheckIdentityConfiguration(ctx context.Context, dsn, fingerprint string) error {
	s, err := New(dsn)
	if err != nil {
		return err
	}
	defer s.Close()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return databaseError("begin identity check", err)
	}
	defer rollback(tx)
	_, _, err = requireIdentityAdmission(ctx, tx, fingerprint)
	return err
}

func installIdentityWriteGuards(ctx context.Context, tx pgx.Tx) error {
	// The variable is transaction-local, set only after matching the persisted
	// declaration. Old binaries cannot insert new grants/permits or turn an old
	// denied request into fresh authority after identity activation.
	_, err := tx.Exec(ctx, `
CREATE OR REPLACE FUNCTION inferplane_identity_admission_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE wanted text; enabled boolean;
BEGIN
 IF TG_OP = 'UPDATE' THEN
   IF NEW.amount <= OLD.amount THEN RETURN NEW; END IF;
 END IF;
 IF to_regclass('identity_mode') IS NULL THEN RETURN NEW; END IF;
 SELECT fingerprint, required::text IN ('true','1') INTO wanted, enabled
   FROM identity_mode LIMIT 1;
 IF COALESCE(enabled, false) AND
   wanted IS DISTINCT FROM current_setting('inferplane.identity_fingerprint', true)
 THEN
   RAISE EXCEPTION 'identity-aware admission required' USING ERRCODE='42501';
 END IF;
 RETURN NEW;
END $$;`)
	if err != nil {
		return databaseError("create identity admission guard", err)
	}
	for _, table := range []string{"authority_requests", "shared_permits"} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			return databaseError("inspect identity guard target", err)
		}
		if !exists {
			continue
		}
		event := "INSERT"
		if table == "authority_requests" {
			event += " OR UPDATE OF amount"
		}
		name := table + "_identity_guard"
		// Identifiers are from the fixed list above, never configuration/input.
		if _, err := tx.Exec(ctx, `DROP TRIGGER IF EXISTS `+name+` ON `+table); err != nil {
			return databaseError("replace identity admission guard", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TRIGGER `+name+` BEFORE `+event+` ON `+table+
			` FOR EACH ROW EXECUTE FUNCTION inferplane_identity_admission_guard()`); err != nil {
			return databaseError("install identity admission guard", err)
		}
	}
	return nil
}

func requireIdentityAdmission(ctx context.Context, tx pgx.Tx, supplied string) (*identity.Config, string, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('identity_mode') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, "", databaseError("inspect identity mode", err)
	}
	if !exists {
		if supplied != "" {
			return nil, "", errors.New("authority identity declaration is unavailable")
		}
		return nil, "", nil
	}
	// Serialize activation against new admission. The keystore also locks
	// existing grant/permit tables before its one-way required-mode transition.
	if _, err := tx.Exec(ctx, `LOCK TABLE identity_mode IN SHARE MODE`); err != nil {
		return nil, "", databaseError("lock identity mode", err)
	}
	cfg := &identity.Config{}
	var wanted string
	var declaration string
	err := tx.QueryRow(ctx, `SELECT organization, required::text IN ('true','1'), fingerprint, declaration FROM identity_mode LIMIT 1`).
		Scan(&cfg.Organization, &cfg.Required, &wanted, &declaration)
	if errors.Is(err, pgx.ErrNoRows) {
		if supplied != "" {
			return nil, "", errors.New("authority identity declaration is unavailable")
		}
		return nil, "", nil
	}
	if err != nil {
		return nil, "", databaseError("read identity mode", err)
	}
	if !cfg.Required {
		if supplied != "" {
			return nil, "", errors.New("authority identity declaration is not active")
		}
		return nil, "", nil
	}
	if wanted == "" || wanted != supplied {
		return nil, "", errors.New("authority identity declaration does not match")
	}
	// Read the immutable migration declaration, not the entire growing
	// identity registry on every request. Canonical enrollments do not change
	// this declaration; the key snapshot independently verifies membership.
	var declared identity.Config
	if err := json.Unmarshal([]byte(declaration), &declared); err != nil ||
		declared.Validate() != nil || !declared.Required || declared.Organization != cfg.Organization ||
		declared.Fingerprint() != wanted {
		return nil, "", errors.New("authority identity declaration is invalid")
	}
	cfg = &declared
	if _, err := tx.Exec(ctx, `SELECT set_config('inferplane.identity_fingerprint',$1,true)`, wanted); err != nil {
		return nil, "", databaseError("bind identity transaction", err)
	}
	return cfg, wanted, nil
}
