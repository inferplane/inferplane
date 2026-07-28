package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/server/authapi"
)

// TestDataMuxCLILoginOff pins the opt-in default (ADR-028): with cliVerifier
// nil (oidc.cli_login absent/disabled), none of the three CLI-login routes
// are mounted — a request to any of them falls through to the ordinary
// KeyAuth-guarded catch-all and 401s like any other unauthenticated request,
// never a CLI-specific 404 that would leak "this feature exists here".
func TestDataMuxCLILoginOff(t *testing.T) {
	store := stubStore{}
	mux := DataMux(nil, newHolder(nil, nil), store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)

	for _, path := range []string{"/v1/auth/config", "/v1/auth/key"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 401 {
			t.Fatalf("%s: got %d, want 401 (falls through to KeyAuth, not mounted)", path, rec.Code)
		}
	}
}

// TestDataMuxCLILoginMintAndRevoke exercises the full opt-in wire: an OIDC
// identity mints a key via POST /v1/auth/key, the minted key then
// authenticates an ordinary data-plane request AND its own DELETE
// /v1/auth/key self-revoke, after which it stops working.
func TestDataMuxCLILoginMintAndRevoke(t *testing.T) {
	store, err := keystore.OpenSQLite(t.TempDir() + "/k.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	v := &fakeVerifier{claims: adminauth.Claims{Subject: "alice", Groups: []string{"team-alpha"}}}
	mapping := adminauth.MappingConfig{GroupMappings: []adminauth.GroupMapping{{Group: "team-alpha", Teams: []string{"alpha"}}}}
	cliConfig := func() authapi.ConfigView {
		return authapi.ConfigView{CLI: true, Issuer: "https://idp.example.com", ClientID: "cli-client"}
	}
	mux := DataMux(nil, newHolder(nil, nil), store, nil, nil, nil, nil, nil, nil, v, mapping, cliConfig, time.Hour)

	// GET /v1/auth/config — unauthenticated discovery.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/auth/config", nil))
	if rec.Code != 200 {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}
	var cfgOut authapi.ConfigView
	json.Unmarshal(rec.Body.Bytes(), &cfgOut)
	if !cfgOut.CLI || cfgOut.ClientID != "cli-client" {
		t.Fatalf("config: %+v", cfgOut)
	}

	// POST /v1/auth/key — OIDC-authenticated mint (fakeVerifier ignores the
	// bearer's actual content and returns its canned claims for any JWT-shaped
	// value, so jwtShaped from adminauth_test.go is enough to route here).
	req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+jwtShaped)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body.String())
	}
	var minted map[string]string
	json.Unmarshal(rec.Body.Bytes(), &minted)
	if minted["team"] != "alpha" || minted["key"] == "" {
		t.Fatalf("mint response: %+v", minted)
	}

	// The minted key authenticates an ordinary data-plane request.
	req2 := httptest.NewRequest("GET", "/v1/usage", nil)
	req2.Header.Set("x-api-key", minted["key"])
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code == 401 {
		t.Fatalf("minted key rejected by KeyAuth: %d %s", rec2.Code, rec2.Body.String())
	}

	// DELETE /v1/auth/key — self-revoke, authenticated with the key itself.
	req3 := httptest.NewRequest("DELETE", "/v1/auth/key", nil)
	req3.Header.Set("x-api-key", minted["key"])
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != 204 {
		t.Fatalf("revoke: %d %s", rec3.Code, rec3.Body.String())
	}

	// The revoked key no longer authenticates anything.
	req4 := httptest.NewRequest("GET", "/v1/usage", nil)
	req4.Header.Set("x-api-key", minted["key"])
	rec4 := httptest.NewRecorder()
	mux.ServeHTTP(rec4, req4)
	if rec4.Code != 401 {
		t.Fatalf("revoked key still authenticates: %d", rec4.Code)
	}
}

// TestDataMuxCLILoginDenialIs403 (audit coverage lives in
// TestCLIDenialEmitterShape below — this checks the HTTP behavior an
// OIDC identity that maps to no team sees end to end).
func TestDataMuxCLILoginDenialIs403(t *testing.T) {
	store := stubStore{}
	v := &fakeVerifier{claims: adminauth.Claims{Subject: "outsider", Groups: []string{"no-such-group"}}}
	mux := DataMux(nil, newHolder(nil, nil), store, nil, nil, nil, nil, nil, nil, v, adminauth.MappingConfig{}, func() authapi.ConfigView { return authapi.ConfigView{} }, time.Hour)

	req := httptest.NewRequest("POST", "/v1/auth/key", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+jwtShaped)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

// TestCLIDenialEmitterShape: cliDenialEmitter is adminDenialEmitter's twin
// for the data-plane mint endpoint — same PII-minimal shape, tagged Ingress
// "cli" and Event "cli_denied" so an operator can tell the two planes apart
// in the audit chain (ADR-028).
func TestCLIDenialEmitterShape(t *testing.T) {
	var got []struct {
		event, ingress, sub string
	}
	emit := cliDenialEmitter(func(r audit.Record) {
		got = append(got, struct{ event, ingress, sub string }{r.Event, r.Request.Ingress, *r.Principal.User})
	})
	emit(httptest.NewRequest("POST", "/v1/auth/key", nil), "outsider")
	if len(got) != 1 || got[0].event != "cli_denied" || got[0].ingress != "cli" || got[0].sub != "outsider" {
		t.Fatalf("cliDenialEmitter record: %+v", got)
	}
}

func TestCLIDenialEmitterNilEmitIsNoop(t *testing.T) {
	if cliDenialEmitter(nil) != nil {
		t.Fatal("nil emit must produce a nil denial hook (AdminAuth treats nil as skip)")
	}
}
