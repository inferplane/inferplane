package authapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/principal"
)

func newTestStore(t *testing.T) *keystore.SQLiteStore {
	t.Helper()
	s, err := keystore.OpenSQLite(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestConfigHandler(t *testing.T) {
	h := NewConfigHandler(func() ConfigView {
		return ConfigView{CLI: true, Issuer: "https://idp.example.com", ClientID: "cli-client"}
	})
	req := httptest.NewRequest("GET", "/v1/auth/config", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	var out ConfigView
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.CLI || out.Issuer != "https://idp.example.com" || out.ClientID != "cli-client" {
		t.Fatalf("config: %+v", out)
	}
}

func TestConfigHandlerRejectsWrite(t *testing.T) {
	h := NewConfigHandler(func() ConfigView { return ConfigView{} })
	req := httptest.NewRequest("POST", "/v1/auth/config", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Fatalf("got %d, want 405", rec.Code)
	}
}

func TestMintHandler_singleTeamAutoResolves(t *testing.T) {
	store := newTestStore(t)
	var recorded []audit.Record
	h := MintHandler(store, time.Hour, limiter.NewMemory(), func(r audit.Record) { recorded = append(recorded, r) })

	id := principal.AdminIdentity{Subject: "alice", Teams: []string{"alpha"}, AuthMethod: "oidc"}
	req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
	req = req.WithContext(principal.WithAdmin(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !strings.HasPrefix(out["key"], "ik_") || out["team"] != "alpha" || out["key_id"] == "" {
		t.Fatalf("mint response: %+v", out)
	}

	// The store, not the client, decided the expiry — must be close to now+ttl.
	p, err := store.Resolve(context.Background(), out["key"])
	if err != nil {
		t.Fatal(err)
	}
	if p.Owner != "alice" {
		t.Fatalf("owner = %q, want caller subject", p.Owner)
	}
	wantExpiry := time.Now().UTC().Add(time.Hour)
	if p.ExpiresAt == nil || p.ExpiresAt.Sub(wantExpiry).Abs() > time.Minute {
		t.Fatalf("expires_at = %v, want ~%v", p.ExpiresAt, wantExpiry)
	}

	if len(recorded) != 1 || recorded[0].Event != "cli_key_created" {
		t.Fatalf("audit: %+v", recorded)
	}
	if recorded[0].Principal.User == nil || *recorded[0].Principal.User != "alice" {
		t.Fatalf("audit user: %+v", recorded[0].Principal)
	}
}

func TestMintHandler_serverDecidesTTLEvenIfClientAsks(t *testing.T) {
	// mintRequest has no ttl/expires_at field at all — a client cannot
	// request a longer-lived key by sending extra JSON fields; they are
	// silently ignored by json.Decode into the narrow mintRequest struct.
	store := newTestStore(t)
	h := MintHandler(store, 15*time.Minute, limiter.NewMemory(), nil)
	id := principal.AdminIdentity{Subject: "alice", Teams: []string{"alpha"}, AuthMethod: "oidc"}
	req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{"team":"alpha","expires_at":"2099-01-01T00:00:00Z","ttl":"999h"}`))
	req = req.WithContext(principal.WithAdmin(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	p, err := store.Resolve(context.Background(), out["key"])
	if err != nil {
		t.Fatal(err)
	}
	if p.ExpiresAt.After(time.Now().UTC().Add(time.Hour)) {
		t.Fatalf("client-supplied ttl/expires_at leaked through: %v", p.ExpiresAt)
	}
}

func TestMintHandler_notEntitledIs403(t *testing.T) {
	store := newTestStore(t)
	h := MintHandler(store, time.Hour, limiter.NewMemory(), nil)
	id := principal.AdminIdentity{Subject: "alice", Teams: []string{"alpha"}, AuthMethod: "oidc"}
	req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{"team":"bravo"}`))
	req = req.WithContext(principal.WithAdmin(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

func TestMintHandler_ambiguousTeamsRequiresExplicitTeam(t *testing.T) {
	store := newTestStore(t)
	h := MintHandler(store, time.Hour, limiter.NewMemory(), nil)
	id := principal.AdminIdentity{Subject: "alice", Teams: []string{"alpha", "bravo"}, AuthMethod: "oidc"}
	req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
	req = req.WithContext(principal.WithAdmin(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestMintHandler_adminMustSpecifyTeam(t *testing.T) {
	store := newTestStore(t)
	h := MintHandler(store, time.Hour, limiter.NewMemory(), nil)
	id := principal.AdminIdentity{Subject: "root", IsAdmin: true, AuthMethod: "oidc"}
	req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
	req = req.WithContext(principal.WithAdmin(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestMintHandler_noIdentityIsForbidden(t *testing.T) {
	store := newTestStore(t)
	h := MintHandler(store, time.Hour, limiter.NewMemory(), nil)
	req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

// TestMintHandler_perSubjectRateLimit (ADR-028 follow-up r1): a valid ID
// token cannot mint an unbounded number of keys — the limiter caps a burst.
func TestMintHandler_perSubjectRateLimit(t *testing.T) {
	store := newTestStore(t)
	mint := limiter.NewMemory()
	h := MintHandler(store, time.Hour, mint, nil)
	id := principal.AdminIdentity{Subject: "alice", Teams: []string{"alpha"}, AuthMethod: "oidc"}

	var lastCode int
	for i := 0; i < 15; i++ {
		req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
		req = req.WithContext(principal.WithAdmin(req.Context(), id))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		lastCode = rec.Code
		if rec.Code == 429 {
			break
		}
	}
	if lastCode != 429 {
		t.Fatalf("burst of 15 mints never hit the rate limit (last code %d)", lastCode)
	}
}

// TestMintHandler_perSubjectRateLimitIsIndependentPerSubject makes sure the
// limiter key is scoped to the caller, not global — a busy subject must not
// starve out a different one.
func TestMintHandler_perSubjectRateLimitIsIndependentPerSubject(t *testing.T) {
	store := newTestStore(t)
	mint := limiter.NewMemory()
	h := MintHandler(store, time.Hour, mint, nil)

	alice := principal.AdminIdentity{Subject: "alice", Teams: []string{"alpha"}, AuthMethod: "oidc"}
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
		req = req.WithContext(principal.WithAdmin(req.Context(), alice))
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	bob := principal.AdminIdentity{Subject: "bob", Teams: []string{"alpha"}, AuthMethod: "oidc"}
	req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
	req = req.WithContext(principal.WithAdmin(req.Context(), bob))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("bob's first mint got %d, should be unaffected by alice's burst", rec.Code)
	}
}

func TestRevokeHandler_selfRevoke(t *testing.T) {
	store := newTestStore(t)
	plaintext, p, err := store.CreateWithOptions(context.Background(), "alpha", []string{"*"}, keystore.KeyOptions{Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	var recorded []audit.Record
	h := RevokeHandler(store, func(r audit.Record) { recorded = append(recorded, r) })

	req := httptest.NewRequest("DELETE", "/v1/auth/key", nil)
	req = req.WithContext(principal.With(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := store.Resolve(context.Background(), plaintext); err == nil {
		t.Fatal("revoked key must not resolve")
	}
	if len(recorded) != 1 || recorded[0].Event != "cli_key_revoked" {
		t.Fatalf("audit: %+v", recorded)
	}
}

func TestRevokeHandler_noPrincipalIsUnauthorized(t *testing.T) {
	store := newTestStore(t)
	h := RevokeHandler(store, nil)
	req := httptest.NewRequest("DELETE", "/v1/auth/key", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}
