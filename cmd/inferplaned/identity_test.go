package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityDeclarationRejectsMalformedOrUnknownInput(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`null`,
		`{"organization":"acme","requird":true}`,
		`{"organization":"acme","required":true} {}`,
		`{"organization":"acme","required":true,"bindings":[{"identity":{"organization":"acme","kind":"human","issuer":"http://idp.example","subject":"s"},"account_ref":"old"}]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "identity.json")
			if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadIdentityDeclaration(p); err == nil {
				t.Fatal("invalid identity declaration accepted")
			}
		})
	}
}

func TestIdentityDeclarationLoadsWithoutCredentials(t *testing.T) {
	p := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(p, []byte(`{"organization":"acme","required":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadIdentityDeclaration(p)
	if err != nil || cfg == nil || !cfg.Required || cfg.Organization != "acme" {
		t.Fatal("valid metadata-only identity declaration refused")
	}
}
