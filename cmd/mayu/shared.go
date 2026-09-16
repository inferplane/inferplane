package main

import (
	"context"
	"fmt"
	"math"
	"slices"

	sharedpg "github.com/inferplane/inferplane/internal/authority/pgstore"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/keystore"
)

type gatewayKeyStore interface {
	keystore.Store
	keystore.TeamStore
	keystore.KeyEnsurer
}

func openGatewayKeys(ctx context.Context, cfg config.KeyStoreConfig) (gatewayKeyStore, error) {
	var store gatewayKeyStore
	var err error
	if cfg.Type == "postgres" {
		if cfg.Identity != nil && cfg.Identity.Required {
			if err := sharedpg.InstallIdentityWriteGuards(ctx, cfg.DSN); err != nil {
				return nil, fmt.Errorf("identity admission guard installation: %w", err)
			}
		}
		store, err = keystore.OpenPostgres(ctx, cfg.DSN)
	} else {
		store, err = keystore.OpenSQLite(cfg.Path)
	}
	if err != nil {
		return nil, err
	}
	declaration := identity.Config{}
	if cfg.Identity != nil {
		declaration = *cfg.Identity
	}
	if err := store.(keystore.IdentityStore).ConfigureIdentity(ctx, declaration); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("key identity configuration: %w", err)
	}
	return store, nil
}

func seedSharedKeys(ctx context.Context, store *keystore.PostgresStore, cfg *config.Config) error {
	var names []string
	for name := range cfg.Teams {
		names = append(names, name)
	}
	slices.Sort(names)
	var teams []keystore.TeamRecord
	for _, name := range names {
		t := cfg.Teams[name]
		limits := governance.PoliciesFromConfig(map[string]governance.ConfigTeam{name: {
			RatePerMin: t.RateLimit.RequestsPerMinute, TokensPerMinute: t.RateLimit.TokensPerMinute,
			TokensPerDay: t.Quota.TokensPerDay, QuotaExceeded: t.Quota.OnExceeded,
			BudgetUSDPerMonth: t.Budget.USDPerMonth, BudgetUSDPerDay: t.Budget.USDPerDay, BudgetExceeded: t.Budget.OnExceeded,
		}})[name]
		teams = append(teams, keystore.TeamRecord{Name: name, AllowedModels: t.AllowedModels, AllowedRegions: t.AllowedRegions,
			RPM: limits.RatePerMin, TPM: limits.TokensPerMinute, TokensPerDay: limits.TokensPerDay,
			QuotaOnExceeded: limits.QuotaExceeded, BudgetUSDMicros: limits.BudgetMicrosPerMonth,
			BudgetUSDMicrosPerDay: limits.BudgetMicrosPerDay, BudgetOnExceeded: limits.BudgetExceeded})
	}
	var keys []keystore.SeedKey
	for _, key := range cfg.VirtualKeys {
		if _, exists := cfg.Teams[key.Team]; !exists {
			_, ok, err := store.GetTeam(ctx, key.Team)
			if err != nil || !ok {
				return fmt.Errorf("shared virtual key references an unavailable team")
			}
		}
		meta := make(map[string]string, len(key.Metadata)+1)
		for k, v := range key.Metadata {
			meta[k] = v
		}
		meta["managed_by"] = "config"
		keys = append(keys, keystore.SeedKey{Plaintext: key.Key, Team: key.Team, AllowedModels: key.AllowedModels,
			Options: keystore.KeyOptions{RPM: key.RPM, TPM: key.TPM, Owner: key.Owner, Metadata: meta,
				Identity:              key.Identity,
				BudgetUSDMicros:       int64(math.Round(key.BudgetUSDPerMonth * 1_000_000)),
				BudgetUSDMicrosPerDay: int64(math.Round(key.BudgetUSDPerDay * 1_000_000))}})
	}
	return store.Seed(ctx, teams, keys)
}

func (g *gateway) validateSharedReload(next *config.Config) error {
	before := g.cfg
	if identityFingerprint(before.KeyStore.Identity) != identityFingerprint(next.KeyStore.Identity) {
		return fmt.Errorf("identity configuration requires restart")
	}
	if before.KeyStore.Type != next.KeyStore.Type || before.KeyStore.Path != next.KeyStore.Path ||
		before.KeyStore.DSN != next.KeyStore.DSN || before.SharedGovernance() != next.SharedGovernance() {
		return fmt.Errorf("key/governance backend configuration requires restart")
	}
	if !before.SharedGovernance() {
		return nil
	}
	a, b := before.ControlPlane, next.ControlPlane
	if a == nil || b == nil || a.URL != b.URL || a.Dataplane != b.Dataplane || a.Token != b.Token ||
		a.RequireSync != b.RequireSync || a.MaxPolicyAgeDuration != b.MaxPolicyAgeDuration {
		return fmt.Errorf("shared authority identity and readiness configuration requires restart")
	}
	return nil
}

func identityFingerprint(cfg *identity.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Fingerprint()
}
