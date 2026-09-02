package main

// mayu update (roadmap ③ phase 2): SIGNED manual self-update. The security
// constraint that shapes it: the control plane — and any URL an operator or
// its updateAdvice hands us — must never be able to push executable content.
// It can only point at a release location; the binary INDEPENDENTLY verifies
// what it finds there against the release public key embedded at build time,
// so a compromised control plane (or mirror) yields "signature verification
// failed", never code execution.
//
// Mechanism (kept dependency-free — crypto/ed25519 is the standard library;
// the sketch's minisign/cosign are formats, this is the same primitive):
//
//	<base>/manifest.json        {"version": "...", "artifacts": {"<os>_<arch>":
//	                             {"file": "...", "sha256": "..."}}}
//	<base>/manifest.json.sig    base64(ed25519.Sign(privKey, manifest bytes))
//	<base>/<artifact file>      the binary, pinned by the manifest's sha256
//
// ONE signature covers the manifest; every artifact is pinned by hash inside
// it — the standard signed-manifest pattern, so a mirror cannot swap one
// platform's binary without breaking the signature chain. Signing happens in
// the release pipeline (scripts/signrelease); the public key is stamped via
// `-ldflags -X main.updatePubKeyHex=<hex>`. A build WITHOUT a stamped key
// (dev builds, forks) REFUSES to update — fail closed, never "trust anything".
//
// The swap is atomic and rootless: download to a sibling temp file in the
// binary's own directory (same filesystem, so rename is atomic), verify,
// rename the running binary to `<exe>.old`, rename the new one into place.
// Nothing is executed — the user (or their supervisor) restarts. `.old` is
// the manual rollback.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// updatePubKeyHex is the hex-encoded ed25519 release public key, stamped by
// the release pipeline via -ldflags. Empty (a dev build, a fork) means
// `mayu update` refuses: an unverifiable update is worse than no update.
var updatePubKeyHex = ""

// maxManifestBytes / maxArtifactBytes bound the downloads: a manifest is a
// few hundred bytes, a mayu binary tens of MB — anything past these is not a
// release, it is a resource-exhaustion attempt.
const (
	maxManifestBytes = 1 << 20   // 1 MiB
	maxArtifactBytes = 512 << 20 // 512 MiB
)

type updateManifest struct {
	Version   string                    `json:"version"`
	Artifacts map[string]updateArtifact `json:"artifacts"`
}

type updateArtifact struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

func updateCmd(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	baseURL := fs.String("url", "", "release base URL (serves manifest.json, manifest.json.sig, and the artifacts)")
	yes := fs.Bool("yes", false, "apply without the confirmation prompt")
	fs.Parse(args)
	if *baseURL == "" {
		return fmt.Errorf("--url is required (the release location; e.g. the URL from the control plane's update advice)")
	}
	return runUpdate(os.Stdout, *baseURL, updatePubKeyHex, version, "", *yes)
}

// runUpdate is the testable core. pubKeyHex and currentVersion are parameters
// so tests can inject a test key; exePath "" means "the running binary".
func runUpdate(out io.Writer, baseURL, pubKeyHex, currentVersion, exePath string, yes bool) error {
	if pubKeyHex == "" {
		return fmt.Errorf("this build carries no release public key (un-stamped build) — it cannot verify an update and refuses to fetch one; update through the channel that installed it")
	}
	pub, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("embedded release public key is malformed — refusing to update")
	}
	if err := checkUpdateURL(baseURL); err != nil {
		return err
	}
	base := strings.TrimSuffix(baseURL, "/")
	client := &http.Client{Timeout: 5 * time.Minute}

	manifest, err := fetchBounded(client, base+"/manifest.json", maxManifestBytes)
	if err != nil {
		return fmt.Errorf("fetch manifest: %w", err)
	}
	sigB64, err := fetchBounded(client, base+"/manifest.json.sig", maxManifestBytes)
	if err != nil {
		return fmt.Errorf("fetch manifest signature: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil {
		return fmt.Errorf("manifest signature is not valid base64")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), manifest, sig) {
		return fmt.Errorf("manifest signature verification FAILED — the release location does not hold a manifest signed by this build's release key; refusing")
	}

	var m updateManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return fmt.Errorf("signed manifest is not valid JSON: %w", err)
	}
	platform := runtime.GOOS + "_" + runtime.GOARCH
	art, ok := m.Artifacts[platform]
	if !ok {
		return fmt.Errorf("release %s carries no artifact for %s", m.Version, platform)
	}
	if m.Version == currentVersion {
		fmt.Fprintf(out, "already on %s — nothing to do\n", currentVersion)
		return nil
	}
	fmt.Fprintf(out, "verified release manifest: %s → %s (%s)\n", currentVersion, m.Version, platform)
	if !yes {
		return fmt.Errorf("rerun with --yes to apply %s (downloads %s, swaps the binary in place, keeps the previous one as .old; nothing restarts automatically)", m.Version, art.File)
	}

	bin, err := fetchBounded(client, base+"/"+art.File, maxArtifactBytes)
	if err != nil {
		return fmt.Errorf("fetch artifact: %w", err)
	}
	sum := sha256.Sum256(bin)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, art.SHA256) {
		return fmt.Errorf("artifact sha256 mismatch (manifest pins %s, downloaded %s) — refusing", art.SHA256, got)
	}

	if exePath == "" {
		exePath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("locate running binary: %w", err)
		}
		if exePath, err = filepath.EvalSymlinks(exePath); err != nil {
			return fmt.Errorf("resolve running binary: %w", err)
		}
	}
	// Sibling temp file in the SAME directory: rename within one filesystem
	// is atomic, and no step here needs privileges beyond owning the install
	// location the binary already runs from.
	tmp := exePath + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return fmt.Errorf("write new binary: %w", err)
	}
	old := exePath + ".old"
	os.Remove(old) // a previous .old may exist; the fresh one supersedes it
	if err := os.Rename(exePath, old); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("set aside current binary: %w", err)
	}
	if err := os.Rename(tmp, exePath); err != nil {
		// Restore the original so the install is never left binary-less.
		os.Rename(old, exePath)
		os.Remove(tmp)
		return fmt.Errorf("swap in new binary: %w", err)
	}
	fmt.Fprintf(out, "updated %s → %s at %s (previous kept as %s) — restart mayu to run it\n", currentVersion, m.Version, exePath, old)
	return nil
}

// checkUpdateURL enforces the transport rule every other outbound credential
// path uses (the ADR-040 precedent): https, or plain http to loopback only —
// a binary fetched over plaintext from the network is an RCE handed to the
// path. The signature check below would still catch tampering, but a
// plaintext channel invites downgrade games; refuse at the door.
func checkUpdateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid --url: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("--url is plain http to a non-loopback host (%s) — use https", host)
	default:
		return fmt.Errorf("--url must be https (or http to loopback), got scheme %q", u.Scheme)
	}
}

func fetchBounded(client *http.Client, url string, max int64) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("GET %s: response exceeds the %d-byte bound", url, max)
	}
	return b, nil
}
