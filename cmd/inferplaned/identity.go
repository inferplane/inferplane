package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	budgetpg "github.com/inferplane/inferplane/internal/authority/pgstore"
	"github.com/inferplane/inferplane/internal/controlplane"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/keystore"
)

func loadIdentityDeclaration(path string) (*identity.Config, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("identity declaration cannot be opened")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("identity declaration is unreadable or exceeds 1 MiB")
	}
	var cfg identity.Config
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return nil, errors.New("invalid identity declaration")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("identity declaration must contain one JSON object")
	}
	if cfg.Organization == "" || cfg.Validate() != nil {
		return nil, errors.New("invalid identity declaration")
	}
	return &cfg, nil
}

func configureControlPlaneIdentity(cp *controlplane.Server, dsn string, cfg *identity.Config) error {
	if err := cp.SetIdentityConfig(cfg); err != nil {
		return fmt.Errorf("identity policy: %w", err)
	}
	if dsn == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if cfg == nil {
		// Check existing persisted mode without creating a new key store for
		// deployments which have never enabled identity.
		return budgetpg.CheckIdentityConfiguration(ctx, dsn, "")
	}
	if cfg.Required {
		if err := budgetpg.InstallIdentityWriteGuards(ctx, dsn); err != nil {
			return err
		}
	}
	store, err := keystore.OpenPostgres(ctx, dsn)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.ConfigureIdentity(ctx, *cfg)
}
