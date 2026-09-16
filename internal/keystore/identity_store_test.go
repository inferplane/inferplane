package keystore

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/identity"
	"github.com/jackc/pgx/v5"
)

type identityHarness struct {
	Store
	ids    IdentityStore
	exec   func(string, ...any) error
	reopen func() Store
}

func identityStores(t *testing.T, test func(*testing.T, identityHarness)) {
	t.Helper()
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var h identityHarness
			switch backend {
			case "sqlite":
				path := filepath.Join(t.TempDir(), "identity.db")
				open := func() Store {
					s, err := OpenSQLite(path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = s.Close() })
					return s
				}
				h.Store, h.reopen = open(), open
				h.exec = func(query string, args ...any) error {
					_, err := h.Store.(*SQLiteStore).db.ExecContext(context.Background(), query, args...)
					return err
				}
			case "postgres":
				dsn := postgresTestDSN(t)
				open := func() Store { return openPostgresTest(t, dsn) }
				h.Store, h.reopen = open(), open
				h.exec = func(query string, args ...any) error {
					_, err := h.Store.(*PostgresStore).db.Exec(context.Background(), query, args...)
					return err
				}
			}
			var ok bool
			h.ids, ok = h.Store.(IdentityStore)
			if !ok {
				t.Fatal("store does not implement managed identity")
			}
			test(t, h)
		})
	}
}

func person(t *testing.T, subject string) identity.ID {
	t.Helper()
	id, err := identity.NewHuman("org", "https://issuer.example", subject)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIdentityStoreRequiredBindingAndRotation(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		alice := person(t, "alice")
		const oldRef = " legacy owner bytes "
		plain, before, err := h.CreateWithOptions(ctx, "team", []string{"*"}, KeyOptions{Owner: oldRef})
		if err != nil {
			t.Fatal(err)
		}
		cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: alice, AccountRef: oldRef}}}
		if err := h.ids.ConfigureIdentity(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		after, err := h.Resolve(ctx, plain)
		if err != nil || after.Identity == nil || *after.Identity != alice || after.AccountSubject() != oldRef ||
			after.KeyID != before.KeyID || !after.IdentityRequired || after.IdentityFingerprint != cfg.Fingerprint() {
			t.Fatalf("trusted binding did not retain attribution: %v", err)
		}
		_, rotated, err := h.CreateWithOptions(ctx, "other-team", []string{"*"}, KeyOptions{Identity: &alice})
		if err != nil || rotated.AccountSubject() != oldRef || rotated.KeyID == before.KeyID {
			t.Fatalf("rotation split the accounting subject: %v", err)
		}
		bob := person(t, "bob")
		_, fresh, err := h.CreateWithOptions(ctx, "team", []string{"*"}, KeyOptions{Identity: &bob})
		if err != nil || fresh.AccountSubject() != bob.CanonicalRef() {
			t.Fatalf("new verified identity was not enrolled canonically: %v", err)
		}
		got, err := h.ids.IdentityConfig(ctx)
		if err != nil || got.Fingerprint() != cfg.Fingerprint() || len(got.Bindings) != 1 {
			t.Fatal("dynamic enrollment changed the configured declaration")
		}
		for _, opts := range []KeyOptions{{Owner: oldRef}, {Identity: &alice, Owner: bob.CanonicalRef()}} {
			if _, _, err := h.CreateWithOptions(ctx, "team", []string{"*"}, opts); err == nil {
				t.Fatal("required issuance accepted owner-only or mismatched identity")
			}
		}
		binding, found, err := h.ids.IdentityByRef(ctx, oldRef)
		if err != nil || !found || binding.Identity != alice {
			t.Fatal("legacy reference lookup lost its exact binding")
		}
		if before.SharedRevision != "" && after.SharedRevision == before.SharedRevision {
			t.Fatal("shared authorization revision did not change on identity activation")
		}
		list, err := h.List(ctx)
		if err != nil || len(list) != 3 {
			t.Fatal("required key listing failed")
		}
		for _, item := range list {
			if !item.IdentityRequired || item.IdentityFingerprint != cfg.Fingerprint() || item.Identity == nil {
				t.Fatal("key listing lost required identity evidence")
			}
		}
	})
}

func TestPostgresIdentitySeedPreservesLegacyFingerprint(t *testing.T) {
	s := openPostgresTest(t, postgresTestDSN(t))
	ctx := context.Background()
	key := SeedKey{Plaintext: "synthetic-identity-seed", Team: "team", AllowedModels: []string{"*"}, Options: KeyOptions{Owner: "old-ref"}}
	if err := s.Seed(ctx, nil, []SeedKey{key}); err != nil {
		t.Fatal(err)
	}
	readFingerprint := func() string {
		var fingerprint string
		if err := s.db.QueryRow(ctx, `SELECT fingerprint FROM keystore_seed_fingerprints WHERE kind='key' AND identity=$1`,
			hashKey(key.Plaintext)).Scan(&fingerprint); err != nil {
			t.Fatal("cannot read original seed fingerprint")
		}
		return fingerprint
	}
	old := readFingerprint()
	alice := person(t, "alice")
	cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: alice, AccountRef: "old-ref"}}}
	if err := s.ConfigureIdentity(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed(ctx, nil, []SeedKey{key}); err != nil {
		t.Fatal("unchanged original declaration failed after trusted binding")
	}
	key.Options.Identity = &alice
	if err := s.Seed(ctx, nil, []SeedKey{key}); err != nil || readFingerprint() != old {
		t.Fatal("typed attachment rewrote or invalidated the original seed fingerprint")
	}
}

func TestPostgresIdentitySeedCannotChangeTypedDeclarationAfterDeletion(t *testing.T) {
	s := openPostgresTest(t, postgresTestDSN(t))
	ctx := context.Background()
	if err := s.ConfigureIdentity(ctx, identity.Config{Organization: "org"}); err != nil {
		t.Fatal(err)
	}
	alice, bob := person(t, "alice"), person(t, "bob")
	key := SeedKey{Plaintext: "synthetic-typed-seed", Team: "team", Options: KeyOptions{Owner: "old-ref", Identity: &alice}}
	if err := s.Seed(ctx, nil, []SeedKey{key}); err != nil {
		t.Fatal(err)
	}
	changed := key
	changed.Options.Identity = &bob
	for _, deleted := range []bool{false, true} {
		if deleted {
			execPostgresTest(t, s, `DELETE FROM keys WHERE key_hash=$1`, hashKey(key.Plaintext))
		}
		if err := s.Seed(ctx, nil, []SeedKey{changed}); !errors.Is(err, ErrSnapshotChanged) {
			t.Fatal("typed seed declaration changed despite immutable bootstrap identity")
		}
	}
	if err := s.Seed(ctx, nil, []SeedKey{key}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, key.Plaintext); !errors.Is(err, ErrKeyNotFound) {
		t.Fatal("unchanged seed revived a deleted key")
	}
}

func TestPostgresIdentityImportRetainsRegistryModeAndRevokedRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "typed-source.db")
	source, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	dead, tombstone, err := source.CreateWithOptions(ctx, "team", nil, KeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Revoke(ctx, tombstone.KeyID); err != nil {
		t.Fatal(err)
	}
	alice, bob := person(t, "alice"), person(t, "bob")
	cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: alice, AccountRef: " old-ref "}}}
	if err := source.ConfigureIdentity(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	live, original, err := source.CreateWithOptions(ctx, "team", nil, KeyOptions{Identity: &alice})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.CreateWithOptions(ctx, "team", nil, KeyOptions{Identity: &bob}); err != nil {
		t.Fatal(err)
	}
	dest := openPostgresTest(t, postgresTestDSN(t))
	got, err := dest.ImportSQLite(ctx, path)
	if err != nil || got.Keys != 3 {
		t.Fatalf("typed import failed: %+v, %v", got, err)
	}
	imported, err := dest.IdentityConfig(ctx)
	if err != nil || imported.Fingerprint() != cfg.Fingerprint() || len(imported.Bindings) != 1 {
		t.Fatal("import lost mode/declaration or confused enrollment with declaration")
	}
	if err := dest.ConfigureIdentity(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	p, err := dest.Resolve(ctx, live)
	if err != nil || p.KeyID != original.KeyID || !reflect.DeepEqual(p.Identity, original.Identity) || p.Owner != original.Owner {
		t.Fatal("import changed credential identity or legacy account reference")
	}
	if _, err := dest.Resolve(ctx, dead); !errors.Is(err, ErrKeyNotFound) {
		t.Fatal("import revived a revoked legacy key")
	}
	if b, found, err := dest.ResolveIdentity(ctx, bob); err != nil || !found || b.AccountRef != bob.CanonicalRef() {
		t.Fatal("import lost canonical enrollment")
	}
	if err := dest.Seed(ctx, nil, []SeedKey{{Plaintext: live, Team: "team", Options: KeyOptions{Owner: original.Owner}}}); err != nil {
		t.Fatal("matching legacy seed could not attach to an already-bound imported credential")
	}
	if got, err := dest.ImportSQLite(ctx, path); err != nil || got != (ImportResult{}) {
		t.Fatalf("typed import retry was not idempotent: %+v, %v", got, err)
	}
}

func TestPostgresIdentityImportRejectsUndeclaredLegacyBinding(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "untrusted-source.db")
	source, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	if err := source.ConfigureIdentity(ctx, identity.Config{Organization: "org"}); err != nil {
		t.Fatal(err)
	}
	// A malformed import must not turn an extra registry row into a trusted
	// legacy binding under an otherwise matching declaration fingerprint.
	if _, err := source.db.ExecContext(ctx, `INSERT INTO identity_registry
		(organization,kind,issuer,subject,account_ref) VALUES('org','human','https://issuer.example','alice','undeclared-ref')`); err != nil {
		t.Fatal(err)
	}
	dest := openPostgresTest(t, postgresTestDSN(t))
	if _, err := dest.ImportSQLite(ctx, path); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatal("import accepted a legacy alias absent from its explicit declaration")
	}
	if mode, err := dest.IdentityConfig(ctx); err != nil || mode.Organization != "" {
		t.Fatal("rejected import changed destination declaration")
	}
}

func TestPostgresIdentityImportRejectsDifferentDeclaration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "source.db")
	source, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	if err := source.ConfigureIdentity(ctx, identity.Config{Organization: "org", Required: true}); err != nil {
		t.Fatal(err)
	}
	dest := openPostgresTest(t, postgresTestDSN(t))
	original := identity.Config{Organization: "another-org", Required: true}
	if err := dest.ConfigureIdentity(ctx, original); err != nil {
		t.Fatal(err)
	}
	if _, err := dest.ImportSQLite(ctx, path); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatal("import crossed organization/declaration namespaces")
	}
	if got, err := dest.IdentityConfig(ctx); err != nil || got.Fingerprint() != original.Fingerprint() {
		t.Fatal("rejected import altered destination configuration")
	}
}

func TestIdentityStoreDormantAttributionDoesNotEnrollOrRename(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		cfg := identity.Config{Organization: "org"}
		if err := h.ids.ConfigureIdentity(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		alice := person(t, "alice")
		plain, _, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Owner: "unverified-old-owner", Identity: &alice})
		if err != nil {
			t.Fatal(err)
		}
		got, err := h.Resolve(ctx, plain)
		if err != nil || got.Identity == nil || *got.Identity != alice || got.AccountSubject() != "unverified-old-owner" || got.IdentityRequired {
			t.Fatal("dormant attribution changed financial identity")
		}
		if _, found, err := h.ids.ResolveIdentity(ctx, alice); err != nil || found {
			t.Fatal("dormant claims silently enrolled a financial binding")
		}
	})
}

func TestIdentityStoreEnsureKeepsDormantVerifiedEvidence(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		if err := h.ids.ConfigureIdentity(ctx, identity.Config{Organization: "org"}); err != nil {
			t.Fatal(err)
		}
		alice, bob := person(t, "alice"), person(t, "bob")
		plain, _, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Owner: "old-ref", Identity: &alice})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Store.(KeyEnsurer).EnsureKey(ctx, plain, "team", nil, KeyOptions{Owner: "old-ref"}); err != nil {
			t.Fatal(err)
		}
		p, err := h.Resolve(ctx, plain)
		if err != nil || p.Identity == nil || *p.Identity != alice {
			t.Fatal("an unchanged legacy declaration erased verified dormant identity")
		}
		if _, err := h.Store.(KeyEnsurer).EnsureKey(ctx, plain, "team", nil, KeyOptions{Owner: "old-ref", Identity: &bob}); err == nil {
			t.Fatal("ensure replaced an existing verified identity")
		}
	})
}

func TestPostgresIdentityRejectsLegacyTransactionAcrossActivation(t *testing.T) {
	s := openPostgresTest(t, postgresTestDSN(t))
	ctx := context.Background()
	old, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal("cannot begin old transaction")
	}
	defer rollbackPostgres(old)
	var required int
	if err := old.QueryRow(ctx, `SELECT required FROM identity_mode`).Scan(&required); err != nil || required != 0 {
		t.Fatal("cannot read pre-activation snapshot")
	}
	if err := s.ConfigureIdentity(ctx, identity.Config{Organization: "org", Required: true}); err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(ctx, `INSERT INTO keys(key_id,key_hash,team,allowed_models,created_at,owner)
		VALUES('old-transaction','old-transaction-hash','team','','now','unbound')`)
	if err == nil {
		t.Fatal("old repeatable-read snapshot bypassed newly required identity")
	}
}

func TestPostgresIdentityActivationWaitsForOldFinancialTransactions(t *testing.T) {
	for _, table := range []string{"authority_requests", "shared_permits"} {
		t.Run(table, func(t *testing.T) {
			s := openPostgresTest(t, postgresTestDSN(t))
			ctx := context.Background()
			execPostgresTest(t, s, `CREATE TABLE authority_requests(id INTEGER); CREATE TABLE shared_permits(id INTEGER)`)
			old, err := s.db.Begin(ctx)
			if err != nil {
				t.Fatal("cannot begin old financial transaction")
			}
			defer rollbackPostgres(old)
			if _, err := old.Exec(ctx, `LOCK TABLE `+table+` IN ROW EXCLUSIVE MODE`); err != nil {
				t.Fatal("cannot hold old financial write lock")
			}
			done := make(chan error, 1)
			go func() {
				done <- s.ConfigureIdentity(ctx, identity.Config{Organization: "org", Required: true})
			}()
			deadline := time.After(3 * time.Second)
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			waiting := false
			for !waiting {
				select {
				case err := <-done:
					t.Fatalf("activation completed before old financial transaction drained: %v", err)
				case <-deadline:
					t.Fatal("activation did not reach the financial write barrier")
				case <-tick.C:
					err := s.db.QueryRow(ctx, `SELECT EXISTS(
						SELECT 1 FROM pg_locks WHERE relation=to_regclass($1)
						AND mode='ExclusiveLock' AND NOT granted)`, table).Scan(&waiting)
					if err != nil {
						t.Fatal("cannot observe activation barrier")
					}
				}
			}
			var required int
			if err := s.db.QueryRow(ctx, `SELECT required FROM identity_mode`).Scan(&required); err != nil || required != 0 {
				t.Fatal("required mode became visible before financial write barrier")
			}
			rollbackPostgres(old)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIdentityStoreActivationIsAtomicAndNeverGuesses(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		plain, _, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Owner: "unknown", Metadata: map[string]string{"source": "cli"}})
		if err != nil {
			t.Fatal(err)
		}
		alice := person(t, "alice")
		cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: alice, AccountRef: "other-ref"}}}
		if err := h.ids.ConfigureIdentity(ctx, cfg); err == nil {
			t.Fatal("unbound active key did not prevent activation")
		}
		mode, err := h.ids.IdentityConfig(ctx)
		if err != nil || mode.Required || mode.Organization != "" {
			t.Fatal("failed activation partially published its mode")
		}
		if _, found, err := h.ids.ResolveIdentity(ctx, alice); err != nil || found {
			t.Fatal("failed activation left a registry entry")
		}
		p, err := h.Resolve(ctx, plain)
		if err != nil || p.Identity != nil || p.AccountSubject() != "unknown" {
			t.Fatal("metadata was treated as proof of a human or service identity")
		}
	})
}

func TestIdentityStoreRejectsConflictsDowngradesAndUnconfiguredReopen(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		alice, bob := person(t, "alice"), person(t, "bob")
		cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: alice, AccountRef: "old-ref"}}}
		if err := h.ids.ConfigureIdentity(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		plain, _, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Identity: &alice})
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []identity.Config{
			{}, {Organization: "org"}, {Organization: "other", Required: true},
			{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: bob, AccountRef: "old-ref"}}},
			{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: alice, AccountRef: "new-ref"}}},
		} {
			if err := h.ids.ConfigureIdentity(ctx, bad); err == nil {
				t.Fatal("conflicting organization, mode or bijection was accepted")
			}
		}
		next := h.reopen()
		if _, err := next.Resolve(ctx, plain); !errors.Is(err, ErrStoreUnavailable) {
			t.Fatalf("required store reopened without configuring its declaration: %v", err)
		}
		if err := next.(IdentityStore).ConfigureIdentity(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := next.Resolve(ctx, plain); err != nil {
			t.Fatal(err)
		}
	})
}

func TestIdentityStoreRequiredWriteGuardsAndRevokedHistory(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		_, old, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Revoke(ctx, old.KeyID); err != nil {
			t.Fatal(err)
		}
		alice := person(t, "alice")
		cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: alice, AccountRef: "old-ref"}}}
		if err := h.ids.ConfigureIdentity(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		_, live, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Identity: &alice})
		if err != nil {
			t.Fatal(err)
		}
		// SQLite FK enforcement defaults off in the legacy opener. These SQL
		// statements model old clients, without relying on application checks.
		for _, query := range []string{
			`INSERT INTO keys(key_id,key_hash,team,allowed_models,created_at,owner) VALUES('old-client','old-hash','team','','now','old-ref')`,
			`INSERT INTO keys(key_id,key_hash,team,allowed_models,created_at,owner,revoked) VALUES('old-dead','old-dead-hash','team','','now','unbound-history',1)`,
			`UPDATE keys SET owner='another-ref' WHERE key_id='` + live.KeyID + `'`,
			`UPDATE keys SET identity_issuer='' WHERE key_id='` + live.KeyID + `'`,
			`UPDATE keys SET revoked=0 WHERE key_id='` + old.KeyID + `'`,
			`UPDATE identity_mode SET required=0`,
			`UPDATE identity_registry SET account_ref='another-ref'`,
			`DELETE FROM identity_registry`,
		} {
			if err := h.exec(query); err == nil {
				t.Fatal("required identity was bypassed by a legacy SQL write")
			}
		}
	})
}

func TestIdentityStoreHistoricalOwnerRequiresExplicitBinding(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		plain, old, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Owner: "historical-owner"})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Revoke(ctx, old.KeyID); err != nil {
			t.Fatal(err)
		}
		cfg := identity.Config{Organization: "org", Required: true}
		if err := h.ids.ConfigureIdentity(ctx, cfg); !errors.Is(err, ErrIdentityNeedsMapping) {
			t.Fatal("revocation hid an unmapped historical accounting subject")
		}
		alice := person(t, "alice")
		cfg.Bindings = []identity.Binding{{Identity: alice, AccountRef: "historical-owner"}}
		if err := h.ids.ConfigureIdentity(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Resolve(ctx, plain); !errors.Is(err, ErrKeyNotFound) {
			t.Fatal("mapping revived a revoked credential")
		}
		_, next, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Identity: &alice})
		if err != nil || next.AccountSubject() != "historical-owner" {
			t.Fatal("mapped history was replaced by a fresh canonical account")
		}
	})
}

func TestIdentityStoreExpiredOwnerRequiresExplicitBinding(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		expired := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
		plain, _, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Owner: "expired-owner", ExpiresAt: &expired})
		if err != nil {
			t.Fatal(err)
		}
		cfg := identity.Config{Organization: "org", Required: true}
		if err := h.ids.ConfigureIdentity(ctx, cfg); !errors.Is(err, ErrIdentityNeedsMapping) {
			t.Fatal("expiry hid an unmapped accounting subject")
		}
		alice := person(t, "alice")
		cfg.Bindings = []identity.Binding{{Identity: alice, AccountRef: "expired-owner"}}
		if err := h.ids.ConfigureIdentity(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Resolve(ctx, plain); !errors.Is(err, ErrKeyExpired) {
			t.Fatal("binding changed credential expiry")
		}
	})
}

func TestIdentityStoreConcurrentEnrollmentUsesOneReference(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		cfg := identity.Config{Organization: "org", Required: true}
		if err := h.ids.ConfigureIdentity(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		alice := person(t, "alice")
		var wg sync.WaitGroup
		errorsCh := make(chan error, 16)
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, p, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Identity: &alice})
				if err == nil && p.AccountSubject() != alice.CanonicalRef() {
					err = errors.New("concurrent enrollment changed reference")
				}
				errorsCh <- err
			}()
		}
		wg.Wait()
		close(errorsCh)
		for err := range errorsCh {
			if err != nil {
				t.Fatal(err)
			}
		}
		c, err := h.ids.IdentityConfig(ctx)
		if err != nil || c.Fingerprint() != cfg.Fingerprint() || len(c.Bindings) != 0 {
			t.Fatal("concurrent enrollment mutated the declaration")
		}
	})
}

func TestSQLiteIdentityReplaceCannotBypassImmutability(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `PRAGMA recursive_triggers=OFF; PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	alice := person(t, "alice")
	cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: alice, AccountRef: "old-ref"}}}
	if err := s.ConfigureIdentity(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT OR REPLACE INTO identity_mode(singleton) VALUES(1)`,
		`INSERT OR REPLACE INTO identity_registry(organization,kind,issuer,subject,account_ref)
		 VALUES('org','human','https://issuer.example','bob','old-ref')`,
	} {
		if _, err := s.db.ExecContext(ctx, query); err == nil {
			t.Fatal("REPLACE bypassed identity immutability with recursive triggers off")
		}
	}
}

func TestIdentityStoreFailedEnsureDoesNotEnrollOrReassign(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		cfg := identity.Config{Organization: "org", Required: true}
		if err := h.ids.ConfigureIdentity(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		alice, bob := person(t, "alice"), person(t, "bob")
		plain, before, err := h.CreateWithOptions(ctx, "team", nil, KeyOptions{Identity: &alice})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Store.(KeyEnsurer).EnsureKey(ctx, plain, "team", nil, KeyOptions{Identity: &bob}); err == nil {
			t.Fatal("existing credential was reassigned to another person")
		}
		if _, found, err := h.ids.ResolveIdentity(ctx, bob); err != nil || found {
			t.Fatal("failed credential write committed its new enrollment")
		}
		after, err := h.Resolve(ctx, plain)
		if err != nil || !reflect.DeepEqual(before.Identity, after.Identity) || before.Owner != after.Owner {
			t.Fatal("failed ensure modified attribution")
		}
		if _, err := h.Store.(KeyEnsurer).EnsureKey(ctx, plain, "team", nil, KeyOptions{Identity: &alice}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestIdentityStoreCanonicalLegacyCollisionRefuses(t *testing.T) {
	identityStores(t, func(t *testing.T, h identityHarness) {
		ctx := context.Background()
		alice, bob := person(t, "alice"), person(t, "bob")
		cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{{Identity: alice, AccountRef: bob.CanonicalRef()}}}
		if err := h.ids.ConfigureIdentity(ctx, cfg); err == nil ||
			strings.Contains(err.Error(), "issuer.example") {
			t.Fatal("canonical collision accepted or identity values leaked")
		}
	})
}
