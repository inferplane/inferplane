package main

// mayu update (roadmap ③ phase 2): the non-negotiable is that no URL — not
// the operator's, not the control plane's updateAdvice — can hand this
// binary executable content that was not signed by the build-embedded
// release key. Every refusal path below is that constraint.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// signedRelease serves a manifest + signature + one artifact for THIS
// platform, signed with a fresh test key. mutate lets a test corrupt one
// piece after signing.
func signedRelease(t *testing.T, version string, artifact []byte, mutate func(files map[string][]byte)) (*httptest.Server, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifact)
	manifest, _ := json.Marshal(map[string]any{
		"version": version,
		"artifacts": map[string]any{
			runtime.GOOS + "_" + runtime.GOARCH: map[string]string{
				"file": "mayu_test_bin", "sha256": hex.EncodeToString(sum[:]),
			},
		},
	})
	files := map[string][]byte{
		"/manifest.json":     manifest,
		"/manifest.json.sig": []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))),
		"/mayu_test_bin":     artifact,
	}
	if mutate != nil {
		mutate(files)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, hex.EncodeToString(pub)
}

func TestUpdateHappyPathSwapsAtomically(t *testing.T) {
	newBin := []byte("#!new-binary-bytes")
	srv, pubHex := signedRelease(t, "9.9.9", newBin, nil)
	dir := t.TempDir()
	exe := filepath.Join(dir, "mayu")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runUpdate(&out, srv.URL, pubHex, "1.0.0", exe, true); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != string(newBin) {
		t.Fatalf("binary not swapped: %q", got)
	}
	old, err := os.ReadFile(exe + ".old")
	if err != nil || string(old) != "old-binary" {
		t.Fatalf("previous binary must be kept as .old: %q %v", old, err)
	}
	if fi, _ := os.Stat(exe); fi.Mode()&0o111 == 0 {
		t.Fatalf("new binary is not executable: %v", fi.Mode())
	}
	if !strings.Contains(out.String(), "restart mayu") {
		t.Fatalf("update must tell the user nothing restarts automatically: %s", out.String())
	}
}

func TestUpdateWithoutYesVerifiesButDoesNotApply(t *testing.T) {
	srv, pubHex := signedRelease(t, "9.9.9", []byte("new"), nil)
	dir := t.TempDir()
	exe := filepath.Join(dir, "mayu")
	os.WriteFile(exe, []byte("old-binary"), 0o755)
	err := runUpdate(io.Discard, srv.URL, pubHex, "1.0.0", exe, false)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without --yes the update must stop after verification: %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
		t.Fatalf("binary must be untouched without --yes: %q", got)
	}
}

func TestUpdateRefusals(t *testing.T) {
	newBin := []byte("new-binary")
	cases := []struct {
		name   string
		mutate func(files map[string][]byte)
		pubHex func(realPub string) string
		url    func(real string) string
		want   string
	}{
		{name: "tampered manifest", want: "signature verification FAILED",
			mutate: func(f map[string][]byte) {
				f["/manifest.json"] = []byte(strings.Replace(string(f["/manifest.json"]), "9.9.9", "6.6.6", 1))
			}},
		{name: "swapped artifact", want: "sha256 mismatch",
			mutate: func(f map[string][]byte) { f["/mayu_test_bin"] = []byte("evil-binary") }},
		{name: "wrong key", want: "signature verification FAILED",
			pubHex: func(string) string {
				other, _, _ := ed25519.GenerateKey(rand.Reader)
				return hex.EncodeToString(other)
			}},
		{name: "unstamped build", want: "no release public key",
			pubHex: func(string) string { return "" }},
		{name: "plain http to non-loopback", want: "plain http",
			url: func(string) string { return "http://releases.example.com/mayu" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, realPub := signedRelease(t, "9.9.9", newBin, tc.mutate)
			pub := realPub
			if tc.pubHex != nil {
				pub = tc.pubHex(realPub)
			}
			url := srv.URL
			if tc.url != nil {
				url = tc.url(srv.URL)
			}
			dir := t.TempDir()
			exe := filepath.Join(dir, "mayu")
			os.WriteFile(exe, []byte("old-binary"), 0o755)
			err := runUpdate(io.Discard, url, pub, "1.0.0", exe, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want refusal containing %q, got %v", tc.want, err)
			}
			if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
				t.Fatalf("a refused update must leave the binary untouched: %q", got)
			}
		})
	}
}

func TestUpdateSameVersionIsANoOp(t *testing.T) {
	srv, pubHex := signedRelease(t, "1.0.0", []byte("new"), nil)
	dir := t.TempDir()
	exe := filepath.Join(dir, "mayu")
	os.WriteFile(exe, []byte("old-binary"), 0o755)
	var out strings.Builder
	if err := runUpdate(&out, srv.URL, pubHex, "1.0.0", exe, true); err != nil {
		t.Fatalf("same-version update must be a clean no-op: %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
		t.Fatalf("same-version update must not touch the binary: %q", got)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Fatalf("no-op must say so: %s", out.String())
	}
}
