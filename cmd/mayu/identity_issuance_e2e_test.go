package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/server"
	"github.com/inferplane/inferplane/internal/server/authapi"
)

// This seam represents successful signature/issuer verification, not client JSON.
// The real verifier's signature/issuer checks have their own adversarial tests.
type issuanceVerifier struct{ issuer string }

func (v issuanceVerifier) Verify(context.Context, string) (adminauth.Claims, error) {
	return adminauth.Claims{Issuer: v.issuer, Subject: "same-subject", Groups: []string{"members"}}, nil
}

func TestIdentityIssuanceMiddlewarePreservesVerifiedIssuerAndBinding(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	first, err := identity.NewHuman("org", "https://first.example", "same-subject")
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"key_store": map[string]any{
		"type": "sqlite", "path": filepath.Join(dir, "keys.db"),
		"identity": identity.Config{Organization: "org", Required: true,
			Bindings: []identity.Binding{{Identity: first, AccountRef: "existing-account"}}},
	}}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openKeysCLI("", cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var mu sync.Mutex
	var records []audit.Record
	mint := authapi.MintHandler(store, time.Hour, limiter.NewMemory(), func(r audit.Record) {
		mu.Lock()
		defer mu.Unlock()
		records = append(records, r)
	})
	var priorKey string
	for i, issuer := range []string{"https://first.example", "https://first.example", "https://second.example"} {
		handler := server.AdminAuth(nil, issuanceVerifier{issuer},
			adminauth.MappingConfig{GroupMappings: []adminauth.GroupMapping{{Group: "members", Teams: []string{"alpha"}}}}, nil, mint)
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/auth/key", strings.NewReader(`{"issuer":"https://forged.example","subject":"victim","owner":"victim"}`))
		req.Header.Set("Authorization", "Bearer synthetic.verified.token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal("local identity mint failed")
		}
		var result map[string]string
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if resp.StatusCode != 200 || decodeErr != nil {
			t.Fatalf("verified middleware mint status=%d", resp.StatusCode)
		}
		p, err := store.Resolve(context.Background(), result["key"])
		if err != nil || p.Identity == nil || p.Identity.Issuer != issuer || p.Identity.Subject != "same-subject" {
			t.Fatal("trusted middleware attribution was lost or replaced")
		}
		if i < 2 && p.AccountSubject() != "existing-account" {
			t.Fatal("legacy accounting reference changed at issuance")
		}
		if i == 1 && p.KeyID == priorKey {
			t.Fatal("second-device issuance reused the credential")
		}
		if i == 2 && (p.AccountSubject() == "existing-account" || p.AccountSubject() != p.Identity.CanonicalRef()) {
			t.Fatal("equal subjects across issuers shared an account")
		}
		priorKey = p.KeyID
	}
	mu.Lock()
	defer mu.Unlock()
	if len(records) != 3 {
		t.Fatal("mint audit events missing")
	}
	for _, record := range records {
		raw, _ := record.Canonical()
		if record.Principal.Identity == nil || strings.Contains(string(raw), "same-subject") || strings.Contains(string(raw), "https://") {
			t.Fatal("mint audit omitted identity evidence or exposed raw identity")
		}
	}
}
