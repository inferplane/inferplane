package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCredentialsRoundTrip(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	want := credentials{
		Gateway:      "https://gw.example.com",
		Issuer:       "https://idp.example.com",
		ClientID:     "cli-client",
		Team:         "alpha",
		Key:          "ik_abc",
		KeyID:        "ik_12345",
		KeyExpiresAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := want.save(); err != nil {
		t.Fatal(err)
	}
	got, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestCredentialsMissingFileIsErrNotLoggedIn(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	_, err := loadCredentials()
	if !errors.Is(err, errNotLoggedIn) {
		t.Fatalf("got %v, want errNotLoggedIn", err)
	}
}

// TestCredentialsFilePermissions uses a not-yet-existing INFERPLANE_HOME
// subdirectory so save() actually exercises its os.MkdirAll(dir, 0o700) path
// — pointing INFERPLANE_HOME at an already-existing directory (e.g. a bare
// t.TempDir()) is a valid override too, but MkdirAll is a no-op on a dir that
// already exists and wouldn't retroactively chmod it (correctly: we must
// never forcibly chmod a directory the user pointed us at).
func TestCredentialsFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are a no-op on windows")
	}
	home := filepath.Join(t.TempDir(), "cfg")
	t.Setenv("INFERPLANE_HOME", home)
	c := credentials{Gateway: "https://gw.example.com"}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	path, err := credentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", fi.Mode().Perm())
	}
	dirFi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirFi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", dirFi.Mode().Perm())
	}
}

func TestCredentialsSaveLeavesNoTempFileResidue(t *testing.T) {
	home := filepath.Join(t.TempDir(), "cfg")
	t.Setenv("INFERPLANE_HOME", home)
	c := credentials{Gateway: "https://gw.example.com"}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "credentials.json" {
		t.Fatalf("directory entries: %v (want exactly credentials.json, no *.tmp residue)", entries)
	}
}

func TestCredentialsSaveOverwritesExisting(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	first := credentials{Gateway: "https://gw.example.com", Team: "alpha", Key: "ik_first"}
	if err := first.save(); err != nil {
		t.Fatal(err)
	}
	second := credentials{Gateway: "https://gw.example.com", Team: "alpha", Key: "ik_second"}
	if err := second.save(); err != nil {
		t.Fatal(err)
	}
	got, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "ik_second" {
		t.Fatalf("key = %q, want overwritten value ik_second", got.Key)
	}
}

func TestDeleteCredentials(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	c := credentials{Gateway: "https://gw.example.com"}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	if err := deleteCredentials(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredentials(); !errors.Is(err, errNotLoggedIn) {
		t.Fatalf("after delete: got %v, want errNotLoggedIn", err)
	}
}

// TestDeleteCredentialsMissingFileIsNotAnError: `inferplane logout` on an
// already-logged-out machine must not fail (ADR-028 — logout is idempotent
// and must succeed offline).
func TestDeleteCredentialsMissingFileIsNotAnError(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	if err := deleteCredentials(); err != nil {
		t.Fatalf("delete on missing file: %v, want nil", err)
	}
}

func TestCredentialsCorruptFileIsHardError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INFERPLANE_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredentials(); err == nil || errors.Is(err, errNotLoggedIn) {
		t.Fatalf("corrupt file: got %v, want a hard parse error (not errNotLoggedIn)", err)
	}
}
