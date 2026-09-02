// Command signrelease is the release-pipeline half of `mayu update`
// (roadmap ③ phase 2): it generates the release keypair and signs a release
// directory's manifest. Pure standard library (crypto/ed25519) — the same
// primitive the binary verifies with; no cosign/minisign dependency.
//
//	signrelease genkey -out release.key
//	    Writes the PRIVATE key (hex seed) to -out (0600) and prints the
//	    PUBLIC key hex — the value CI stamps into mayu via
//	    `-ldflags "-X main.updatePubKeyHex=<hex>"`. Keep the private key in
//	    the release secret store; it never ships.
//
//	signrelease sign -key release.key -dir <releasedir> -version <v>
//	    Hashes every mayu_<os>_<arch> artifact in -dir into manifest.json
//	    (sha256-pinned) and writes manifest.json.sig (base64 ed25519 over
//	    the exact manifest bytes). Serve the directory as-is; `mayu update
//	    --url <dir's URL>` verifies the chain end to end.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: signrelease genkey|sign [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "genkey":
		err = genkey(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: signrelease genkey|sign [flags]")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func genkey(args []string) error {
	fs := flag.NewFlagSet("genkey", flag.ExitOnError)
	out := fs.String("out", "release.key", "private-key output path (hex seed, 0600)")
	fs.Parse(args)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("private key written to %s (keep it in the release secret store)\n", *out)
	fmt.Printf("public key (stamp into mayu): -ldflags \"-X main.updatePubKeyHex=%s\"\n", hex.EncodeToString(pub))
	return nil
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := fs.String("key", "release.key", "private-key path (hex seed from genkey)")
	dir := fs.String("dir", ".", "release directory holding the mayu_<os>_<arch> artifacts")
	version := fs.String("version", "", "release version (required)")
	fs.Parse(args)
	if *version == "" {
		return fmt.Errorf("-version is required")
	}
	seedHex, err := os.ReadFile(*keyPath)
	if err != nil {
		return err
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(seedHex)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("private key at %s is not a valid hex ed25519 seed", *keyPath)
	}
	priv := ed25519.NewKeyFromSeed(seed)

	entries, err := os.ReadDir(*dir)
	if err != nil {
		return err
	}
	artifacts := map[string]map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "mayu_") {
			continue
		}
		// mayu_<os>_<arch>[.exe] → platform key "<os>_<arch>".
		platform := strings.TrimSuffix(strings.TrimPrefix(name, "mayu_"), ".exe")
		b, err := os.ReadFile(filepath.Join(*dir, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		artifacts[platform] = map[string]string{"file": name, "sha256": hex.EncodeToString(sum[:])}
	}
	if len(artifacts) == 0 {
		return fmt.Errorf("no mayu_<os>_<arch> artifacts found in %s", *dir)
	}
	manifest, err := json.MarshalIndent(map[string]any{"version": *version, "artifacts": artifacts}, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*dir, "manifest.json"), manifest, 0o644); err != nil {
		return err
	}
	sig := ed25519.Sign(priv, manifest)
	if err := os.WriteFile(filepath.Join(*dir, "manifest.json.sig"), []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("signed manifest for %s: %d artifact(s) in %s\n", *version, len(artifacts), *dir)
	return nil
}
