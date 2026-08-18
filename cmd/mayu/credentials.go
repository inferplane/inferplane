package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// credentials is `mayu login`'s on-disk session (ADR-028). It holds NO
// IdP refresh token — only the currently-minted, short-lived gateway virtual
// key plus the TOFU-pinned issuer/client_id used to detect a gateway swap.
// Losing this file to disk theft costs an attacker one short-lived,
// gateway-revocable, team-scoped key — never SSO.
type credentials struct {
	Gateway  string `json:"gateway"`   // data-plane base URL (also ANTHROPIC_BASE_URL)
	Issuer   string `json:"issuer"`    // TOFU-pinned at first login
	ClientID string `json:"client_id"` // TOFU-pinned at first login
	// IDTokenCommand, when non-empty, is the `--id-token-command` used at
	// login — the ONLY way `token` can silently renew past expiry: there is
	// no IdP refresh token cached here (see the type doc above), so without
	// this a renewal needs another interactive `mayu login`.
	IDTokenCommand string    `json:"id_token_command,omitempty"`
	Team           string    `json:"team"`
	Key            string    `json:"key"` // plaintext ik_...
	KeyID          string    `json:"key_id"`
	KeyExpiresAt   time.Time `json:"key_expires_at"`
}

// errNotLoggedIn is returned by loadCredentials when no credential file
// exists, so callers can print a distinct "run mayu login" hint rather
// than a raw os.ErrNotExist.
var errNotLoggedIn = errors.New("not logged in; run: mayu login --gateway <url>")

// credentialsPath resolves the on-disk location: INFERPLANE_HOME, if set, IS
// the directory credentials.json lives in directly — no "/inferplane" suffix
// is appended, so a caller pointing it at an existing directory never has
// that directory's own permissions altered (tests set this for a hermetic,
// XDG-independent path — os.UserConfigDir() ignores XDG_CONFIG_HOME on
// darwin). Otherwise: os.UserConfigDir()+"/inferplane".
func credentialsPath() (string, error) {
	dir := os.Getenv("INFERPLANE_HOME")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve config dir: %w", err)
		}
		dir = filepath.Join(base, "inferplane")
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// loadCredentials reads and parses the credential file. A missing file
// returns errNotLoggedIn (errors.Is-comparable); any other read/parse error
// is wrapped and returned as-is (fail closed — a corrupt file is not silently
// treated as "logged out").
func loadCredentials() (credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return credentials{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return credentials{}, errNotLoggedIn
		}
		return credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	var c credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return credentials{}, fmt.Errorf("parse credentials %s: %w", path, err)
	}
	return c, nil
}

// save persists c atomically: write to a randomly-named temp file in the same
// directory (O_EXCL — a fixed ".tmp" name would make the "atomic" rename a
// symlink/TOCTOU target), fsync, then rename over any existing file. The
// directory is created 0700 and the file 0600; on Windows these modes are a
// no-op (documented caveat, same posture as ~/.aws/credentials).
func (c credentials) save() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp credentials file: %w", err)
	}
	tmpPath := tmp.Name()
	// Defensive cleanup on any early return; a successful path below still
	// closes tmp explicitly before the rename (a file can't be renamed while
	// held open on Windows), so this Remove is a no-op there.
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp credentials file: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp credentials file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp credentials file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp credentials file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename credentials file: %w", err)
	}
	return nil
}

// deleteCredentials removes the credential file. Missing-file is not an
// error — `mayu logout` on an already-logged-out machine is a no-op,
// not a failure.
func deleteCredentials() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove credentials file: %w", err)
	}
	return nil
}
