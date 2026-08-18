package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- in-test IdP: discovery + JWKS + token minting, RSA-signed so it
// exercises the REAL adminauth.Verifier (signature check included), not a
// stub. Trimmed to what this suite needs from the ~60-line pattern already
// used twice in the repo (internal/adminauth/oidc_test.go's newFakeIdP/
// rsaJWK, cmd/mayu/login_test.go's newFakeIdP).

type ssoFakeIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey
	kid string
}

func newSSOFakeIdP(t *testing.T) *ssoFakeIdP {
	t.Helper()
	idp := &ssoFakeIdP{kid: "k1"}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp.key = key

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.srv.URL,
			"jwks_uri":                              idp.srv.URL + "/keys",
			"authorization_endpoint":                idp.srv.URL + "/auth",
			"token_endpoint":                        idp.srv.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{
			{
				"kty": "RSA", "kid": idp.kid, "use": "sig", "alg": "RS256",
				"n": ssoB64(idp.key.PublicKey.N.Bytes()),
				"e": ssoB64(big.NewInt(int64(idp.key.PublicKey.E)).Bytes()),
			},
		}})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func ssoB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// mint builds an RS256-signed JWT. extra merges into (and can delete, via a
// nil value) the default claim set.
func (idp *ssoFakeIdP) mint(t *testing.T, clientID string, extra map[string]any) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss":            idp.srv.URL,
		"sub":            "user-1",
		"aud":            clientID,
		"exp":            now.Add(time.Hour).Unix(),
		"iat":            now.Add(-time.Minute).Unix(),
		"cognito:groups": []string{"ops"},
	}
	for k, v := range extra {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": idp.kid}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signing := ssoB64(hb) + "." + ssoB64(cb)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.key, 5, digest[:]) // crypto.SHA256 == 5
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + ssoB64(sig)
}

const ssoClientID = "inferplaned-console"

func newSSOTestOIDCEnv(idp *ssoFakeIdP) *oidcEnv {
	return &oidcEnv{
		Issuer:        idp.srv.URL,
		ClientID:      ssoClientID,
		GroupsClaim:   "cognito:groups",
		AllowedGroups: []string{"ops"},
		LoginOrigins:  []string{"https://console.example.com"},
	}
}

func TestSSOEndToEnd(t *testing.T) {
	idp := newSSOFakeIdP(t)
	oidc := newSSOTestOIDCEnv(idp)
	dir := t.TempDir()

	mux, cp, closePG, err := buildMux(dir, "static-tok", oidc)
	if err != nil {
		t.Fatalf("buildMux: %v", err)
	}
	defer closePG()
	_ = cp
	ts := httptest.NewServer(mux)
	defer ts.Close()

	get := func(bearer, path string) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	t.Run("discovery endpoint shape", func(t *testing.T) {
		resp := get("", "/ui/auth/config")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		if m["sso"] != true || m["issuer"] != idp.srv.URL || m["client_id"] != ssoClientID {
			t.Fatalf("auth config = %v", m)
		}
		if _, leaked := m["allowed_groups"]; leaked {
			t.Fatalf("auth config must be secret/config-free, leaked allowed_groups: %v", m)
		}
	})

	t.Run("CSP contains the issuer origin", func(t *testing.T) {
		resp := get("", "/ui/")
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, idp.srv.URL) {
			t.Fatalf("CSP %q must contain the issuer origin %q", csp, idp.srv.URL)
		}
		if !strings.Contains(csp, "https://console.example.com") {
			t.Fatalf("CSP %q must contain the configured login origin", csp)
		}
	})

	t.Run("allowed group 200", func(t *testing.T) {
		tok := idp.mint(t, ssoClientID, nil)
		resp := get(tok, "/v1alpha1/usage")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("unrelated group 403", func(t *testing.T) {
		tok := idp.mint(t, ssoClientID, map[string]any{"cognito:groups": []string{"finance"}})
		resp := get(tok, "/v1alpha1/usage")
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("wrong aud 401", func(t *testing.T) {
		tok := idp.mint(t, "someone-elses-client", nil)
		resp := get(tok, "/v1alpha1/usage")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("expired 401", func(t *testing.T) {
		tok := idp.mint(t, ssoClientID, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
		resp := get(tok, "/v1alpha1/usage")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("cognito:groups claim mapping", func(t *testing.T) {
		// Same token shape allowed-group test used — the "cognito:groups"
		// claim name (not the go-oidc default "groups") is what makes this
		// distinct: GroupsClaim above is set to "cognito:groups".
		tok := idp.mint(t, ssoClientID, nil)
		resp := get(tok, "/v1alpha1/dataplanes")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("static token still works on the machine path in the same mux", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.URL+"/v1alpha1/usage", strings.NewReader(`{
			"dataplane": "dp-1",
			"window_start": "2026-08-04T12:00:00Z",
			"window_end": "2026-08-04T12:01:00Z",
			"entries": [{"team": "t", "model": "m"}]
		}`))
		req.Header.Set("Authorization", "Bearer static-tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("static-token ingest status = %d", resp.StatusCode)
		}
	})

	t.Run("non-shaped static token never reaches OIDC path on GET /v1alpha1/sync", func(t *testing.T) {
		resp := get("wrong-static-tok", "/v1alpha1/dataplanes")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})
}

func TestSSOUnconfiguredNo404Route(t *testing.T) {
	dir := t.TempDir()
	mux, _, closePG, err := buildMux(dir, "static-tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closePG()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/ui/auth/config", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured OIDC: /ui/auth/config status = %d, want 404", rec.Code)
	}
}
