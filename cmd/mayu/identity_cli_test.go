package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/identity"
)

func captureIdentityCLI(t *testing.T, args []string) (string, string, error) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		outW.Close()
		t.Fatal(err)
	}
	defer errR.Close()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		outW.Close()
		errW.Close()
	}()
	runErr := keysCreate(args)
	outW.Close()
	errW.Close()
	stdout, _ := io.ReadAll(outR)
	stderr, _ := io.ReadAll(errR)
	return string(stdout), string(stderr), runErr
}

func TestIdentityCLIServiceAccountRotationAndRequiredRefusal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "keys.json")
	body, _ := json.Marshal(map[string]any{
		"key_store": map[string]any{"type": "sqlite", "path": filepath.Join(dir, "keys.db"),
			"identity": identity.Config{Organization: "org", Required: true}},
		// The limited CLI loader must not resolve unrelated provider secrets.
		"providers": map[string]any{"unused": map[string]any{"api_key_ref": map[string]string{"file": "/does-not-exist/unused-secret"}}},
	})
	if err := os.WriteFile(cfgPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureIdentityCLI(t, []string{"--config", cfgPath, "--team", "alpha"}); err == nil {
		t.Fatal("required-mode ownerless CLI mint must refuse")
	}
	want, err := identity.NewService("org", "build-bot")
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	for i := 0; i < 2; i++ {
		out, log, err := captureIdentityCLI(t, []string{"--config", cfgPath, "--team", "alpha", "--service-account", "build-bot"})
		if err != nil {
			t.Fatalf("service CLI failed: %v", err)
		}
		key := strings.SplitN(out, "\n", 2)[0]
		if !strings.HasPrefix(key, "ik_") || strings.Contains(log, key) {
			t.Fatal("CLI plaintext must appear only on stdout")
		}
		store, err := openKeysCLI("", cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		p, err := store.Resolve(context.Background(), key)
		store.Close()
		if err != nil || p.Identity == nil || *p.Identity != want || p.Owner != want.CanonicalRef() {
			t.Fatal("CLI did not persist stable service identity/account")
		}
		if i == 1 && p.KeyID == previous {
			t.Fatal("rotation reused the same credential")
		}
		previous = p.KeyID
	}
}

func TestIdentityCLILegacyCreationRemainsAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	if _, _, err := captureIdentityCLI(t, []string{"--store", path, "--team", "alpha", "--service-account", "bot"}); err == nil {
		t.Fatal("service identity without configured organization must refuse")
	}
	if _, _, err := captureIdentityCLI(t, []string{"--store", path, "--team", "alpha"}); err != nil {
		t.Fatalf("unconfigured legacy creation changed: %v", err)
	}
}
