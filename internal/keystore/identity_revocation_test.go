package keystore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/identity"
)

func requiredRevokedKey(t *testing.T, h identityHarness, revokedBeforeBinding bool) (string, postgresKey) {
	t.Helper()
	ctx := context.Background()
	opts := completeKeyOptions()
	opts.Owner = "legacy-revoked-owner"
	plain, p, err := h.CreateWithOptions(ctx, "team", []string{"model-a", "model-b"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if revokedBeforeBinding {
		if err := h.Revoke(ctx, p.KeyID); err != nil {
			t.Fatal(err)
		}
	}
	alice := person(t, "alice")
	cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: alice, AccountRef: opts.Owner}}}
	if err := h.ids.ConfigureIdentity(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if !revokedBeforeBinding {
		if err := h.Revoke(ctx, p.KeyID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Resolve(ctx, plain); !errors.Is(err, ErrKeyNotFound) {
		t.Fatal("fixture did not revoke the bound credential")
	}
	k := revocationRow(t, h, p.KeyID)
	if k.Revoked != 1 || k.Owner != opts.Owner ||
		k.IdentityOrganization != alice.Organization || k.IdentityKind != string(alice.Kind) ||
		k.IdentityIssuer != alice.Issuer || k.IdentitySubject != alice.Subject {
		t.Fatal("fixture did not retain the revoked key's valid registry binding")
	}
	if s, ok := h.Store.(*SQLiteStore); ok {
		if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF; PRAGMA recursive_triggers=OFF`); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"foreign_keys", "recursive_triggers"} {
			var enabled int
			if err := s.db.QueryRowContext(ctx, `PRAGMA `+name).Scan(&enabled); err != nil || enabled != 0 {
				t.Fatal("fixture did not disable optional SQLite enforcement")
			}
		}
	}
	return plain, k
}

func revocationRow(t *testing.T, h identityHarness, id string) postgresKey {
	t.Helper()
	const query = `SELECT key_id,key_hash,team,allowed_models,created_at,revoked,
		budget_usd_micros,tpm,rpm,expires_at,owner,metadata,budget_usd_micros_per_day,
		identity_organization,identity_kind,identity_issuer,identity_subject FROM keys WHERE key_id=$1`
	var k postgresKey
	var err error
	switch s := h.Store.(type) {
	case *SQLiteStore:
		err = s.db.QueryRowContext(context.Background(), query, id).Scan(k.destinations()...)
	case *PostgresStore:
		err = s.db.QueryRow(context.Background(), query, id).Scan(k.destinations()...)
	}
	if err != nil {
		t.Fatal("cannot read complete revoked key row")
	}
	return k
}

func assertRevokedKeyUnchanged(t *testing.T, h identityHarness, plain string, before postgresKey) {
	t.Helper()
	if after := revocationRow(t, h, before.KeyID); after != before {
		t.Fatal("rejected write changed the tombstone or another key field")
	}
	if _, err := h.Resolve(context.Background(), plain); !errors.Is(err, ErrKeyNotFound) {
		t.Fatal("revoked credential became usable")
	}
}

func TestIdentityStoreRequiredRevocationCannotBeCleared(t *testing.T) {
	for _, when := range []string{"before_binding", "after_binding"} {
		t.Run(when, func(t *testing.T) {
			identityStores(t, func(t *testing.T, h identityHarness) {
				plain, before := requiredRevokedKey(t, h, when == "before_binding")
				if err := h.exec(`UPDATE keys SET revoked=0 WHERE key_id=$1`, before.KeyID); err == nil {
					t.Fatal("old-column UPDATE revived a bound revoked credential")
				}
				assertRevokedKeyUnchanged(t, h, plain, before)
			})
		})
	}
}

func TestSQLiteIdentityRequiredRevocationRejectsReplace(t *testing.T) {
	for _, when := range []string{"before_binding", "after_binding"} {
		for _, collision := range []string{"same_id_and_hash", "same_hash_different_id", "same_id_different_hash", "move_revoked_id", "move_revoked_hash"} {
			t.Run(when+"/"+collision, func(t *testing.T) {
				s := openTest(t)
				h := identityHarness{Store: s, ids: s}
				plain, before := requiredRevokedKey(t, h, when == "before_binding")
				incoming := before
				incoming.Revoked = 0
				if collision == "same_hash_different_id" || collision == "move_revoked_id" {
					incoming.KeyID = "different-synthetic-key-id"
				}
				if collision == "same_id_different_hash" || collision == "move_revoked_hash" {
					incoming.Hash = hashKey("different-synthetic-credential")
				}
				if strings.HasPrefix(collision, "move_revoked_") {
					incoming.Revoked = 1
				}
				query := strings.Replace(postgresInsertKey, "INSERT INTO", "INSERT OR REPLACE INTO", 1)
				if _, err := s.db.ExecContext(context.Background(), query, incoming.values()...); err == nil {
					t.Fatal("REPLACE revived a bound revoked credential")
				}
				assertRevokedKeyUnchanged(t, h, plain, before)
			})
		}
	}
}

func TestIdentityStoreRequiredRevocationRejectsUpsert(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		plain, before := requiredRevokedKey(t, h, true)
		incoming := before
		incoming.KeyID = "different-synthetic-key-id"
		incoming.Revoked = 0
		query := postgresInsertKey + ` ON CONFLICT(key_hash) DO UPDATE SET revoked=excluded.revoked`
		if err := h.exec(query, incoming.values()...); err == nil {
			t.Fatal("upsert revived a bound revoked credential through its hash")
		}
		assertRevokedKeyUnchanged(t, h, plain, before)
	})
}

func TestIdentityStoreRequiredRevocationCannotMoveTombstone(t *testing.T) {
	for _, column := range []string{"key_id", "key_hash"} {
		t.Run(column, func(t *testing.T) {
			identityStores(t, func(t *testing.T, h identityHarness) {
				plain, before := requiredRevokedKey(t, h, true)
				// Moving the tombstone would free the old identifier/hash for an
				// active INSERT, evading a check of revoked alone.
				if err := h.exec(`UPDATE keys SET `+column+`=$1 WHERE key_id=$2`, "moved-synthetic-value", before.KeyID); err == nil {
					t.Fatal("UPDATE moved a tombstone away from its revoked credential")
				}
				assertRevokedKeyUnchanged(t, h, plain, before)
			})
		})
	}
}

func TestIdentityStoreRequiredRevocationPreservesSafeWrites(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		plain, before := requiredRevokedKey(t, h, true)
		if err := h.Revoke(ctx, before.KeyID); err != nil {
			t.Fatal("idempotent revocation failed")
		}
		alice := person(t, "alice")
		opts := completeKeyOptions()
		opts.Owner, opts.Identity = before.Owner, &alice
		if _, err := h.Store.(KeyEnsurer).EnsureKey(ctx, plain, before.Team, []string{"model-a", "model-b"}, opts); err != nil {
			t.Fatal("unchanged EnsureKey could not preserve its tombstone")
		}
		if s, ok := h.Store.(*PostgresStore); ok {
			key := SeedKey{Plaintext: plain, Team: before.Team, AllowedModels: []string{"model-a", "model-b"}, Options: opts}
			if err := s.Seed(ctx, nil, []SeedKey{key}); err != nil {
				t.Fatal("unchanged seed could not attach to its tombstone")
			}
		}
		assertRevokedKeyUnchanged(t, h, plain, before)
		rotated, p, err := h.CreateWithOptions(ctx, before.Team, nil, KeyOptions{Identity: &alice})
		if err != nil || p.KeyID == before.KeyID || p.AccountSubject() != before.Owner {
			t.Fatal("revocation prevented a separate credential for the same account")
		}
		if _, err := h.Resolve(ctx, rotated); err != nil {
			t.Fatal("new credential for the same identity was not usable")
		}
	})
}

func TestIdentityStoreOptionalRevocationKeepsLegacyBehavior(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		if err := h.ids.ConfigureIdentity(ctx, identity.Config{Organization: "org"}); err != nil {
			t.Fatal(err)
		}
		alice := person(t, "alice")
		plain, p, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Owner: "legacy-owner", Identity: &alice})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Revoke(ctx, p.KeyID); err != nil {
			t.Fatal(err)
		}
		if err := h.exec(`UPDATE keys SET revoked=0 WHERE key_id=$1`, p.KeyID); err != nil {
			t.Fatal("required-only guard changed optional legacy behavior")
		}
		if _, err := h.Resolve(ctx, plain); err != nil {
			t.Fatal("optional legacy credential behavior changed")
		}
	})
}
