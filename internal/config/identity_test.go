package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inferplane/inferplane/internal/identity"
)

func TestKeyStoreIdentityRejectsInvalidDeclarations(t *testing.T) {
	for _, declaration := range []string{
		`{"required":true}`,
		`{"organization":"acme","required":true,"bindings":[{"identity":{"organization":"other","kind":"human","issuer":"https://idp.example","subject":"sub"},"account_ref":"legacy"}]}`,
		`{"organization":"acme","required":true,"bindings":[{"identity":{"organization":"acme","kind":"human","issuer":"http://idp.example","subject":"sub"},"account_ref":"legacy"}]}`,
		`{"organization":"acme","required":true,"bindings":[{"identity":{"organization":"acme","kind":"human","issuer":"https://idp.example","subject":"first"},"account_ref":"legacy"},{"identity":{"organization":"acme","kind":"human","issuer":"https://idp.example","subject":"second"},"account_ref":"legacy"}]}`,
	} {
		t.Run(declaration, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keys.json")
			if err := os.WriteFile(path, []byte(`{"key_store":{"type":"sqlite","path":"keys.db","identity":`+declaration+`}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadKeyStore(path); err == nil {
				t.Fatal("invalid identity declaration accepted")
			}
		})
	}
}

func TestRequiredIdentityNeedsAuthenticatedProtectedSynchronization(t *testing.T) {
	for _, tc := range []struct {
		name, url, token string
		sync, ok         bool
	}{
		{"https", "https://cp.example.invalid", "machine", true, true},
		{"loopback", "http://127.0.0.1:7601", "machine", true, true},
		{"remote-http", "http://cp.example.invalid", "machine", true, false},
		{"missing-sync", "https://cp.example.invalid", "machine", false, false},
		{"empty-token", "https://cp.example.invalid", "", true, false},
		{"jwt-token", "https://cp.example.invalid", "eyJhbGciOiJub25lIn0.e30.signature", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				KeyStore: KeyStoreConfig{Identity: &identity.Config{Organization: "acme", Required: true}},
				ControlPlane: &ControlPlaneConfig{
					URL: tc.url, RequireSync: tc.sync, Token: tc.token,
					TokenRef: &SecretRef{Env: "SYNTHETIC_IDENTITY_MACHINE"},
				},
			}
			if err := validateSharedStores(cfg); (err == nil) != tc.ok {
				t.Fatalf("identity synchronization accepted=%v, want %v", err == nil, tc.ok)
			}
		})
	}
}

func TestKeyStoreIdentityDoesNotResolveUnrelatedSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte(`{
		"key_store":{"type":"sqlite","path":"keys.db","identity":{"organization":"acme","required":true}},
		"providers":{"unused":{"api_key_ref":{"env":"MISSING_IDENTITY_TEST_PROVIDER_SECRET"}}},
		"control_plane":{"token_ref":{"env":"MISSING_IDENTITY_TEST_CP_SECRET"}}
	}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyStore(path); err != nil {
		t.Fatalf("unrelated settings blocked identity key management: %v", err)
	}
}
