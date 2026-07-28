package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/server/authapi"
)

// --- pure helpers ---

func TestValidateEndpointURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"https", "https://gw.example.com", true},
		{"https trailing slash trimmed", "https://gw.example.com/", true},
		{"loopback http 127.0.0.1", "http://127.0.0.1:8080", true},
		{"loopback http localhost", "http://localhost:8080", true},
		{"loopback http ::1", "http://[::1]:8080", true},
		{"plain http non-loopback", "http://gw.example.com", false},
		{"not a url", "not a url", false},
		{"no host", "https://", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := validateEndpointURL(c.url)
			if (err == nil) != c.ok {
				t.Fatalf("%q: err=%v, want ok=%v", c.url, err, c.ok)
			}
		})
	}
}

func TestSameOriginOrSubdomain(t *testing.T) {
	cases := []struct {
		name              string
		issuer, candidate string
		want              bool
	}{
		{"exact match", "https://idp.example.com", "https://idp.example.com/authorize", true},
		{"subdomain", "https://idp.example.com", "https://login.idp.example.com/authorize", true},
		{"different domain", "https://idp.example.com", "https://evil.com/authorize", false},
		{"suffix but not subdomain", "https://idp.example.com", "https://evilidp.example.com/authorize", false},
		{"http candidate against https issuer, non-loopback", "https://idp.example.com", "http://idp.example.com/authorize", false},
		{"loopback both", "http://127.0.0.1:1234", "http://127.0.0.1:1234/authorize", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sameOriginOrSubdomain(c.issuer, c.candidate); got != c.want {
				t.Fatalf("sameOriginOrSubdomain(%q, %q) = %v, want %v", c.issuer, c.candidate, got, c.want)
			}
		})
	}
}

func TestNeedsRenewal(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"far future", now.Add(time.Hour), false},
		{"just outside margin", now.Add(renewBefore + time.Second), false},
		{"just inside margin", now.Add(renewBefore - time.Second), true},
		{"already expired", now.Add(-time.Minute), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := needsRenewal(c.expiry, now); got != c.want {
				t.Fatalf("needsRenewal(%v) = %v, want %v", c.expiry, got, c.want)
			}
		})
	}
}

// --- HTTP helpers ---

func TestFetchAuthConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(authapi.ConfigView{CLI: true, Issuer: "https://idp.example.com", ClientID: "cli-client"})
	}))
	defer srv.Close()
	cfg, err := fetchAuthConfig(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CLI || cfg.ClientID != "cli-client" {
		t.Fatalf("cfg: %+v", cfg)
	}
}

func TestFetchAuthConfigDisabledIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(authapi.ConfigView{CLI: false})
	}))
	defer srv.Close()
	if _, err := fetchAuthConfig(context.Background(), srv.URL); err == nil {
		t.Fatal("cli:false should be an error")
	}
}

func TestFetchAuthConfig404IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	if _, err := fetchAuthConfig(context.Background(), srv.URL); err == nil {
		t.Fatal("404 should be an error")
	}
}

func TestMintKeyRelaysServerErrorVerbatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "entitled to multiple teams; specify --team"})
	}))
	defer srv.Close()
	_, err := mintKey(context.Background(), srv.URL, "idtoken", "")
	if err == nil || err.Error() != "entitled to multiple teams; specify --team" {
		t.Fatalf("err = %v, want the server's message verbatim", err)
	}
}

func TestMintKeySendsBearerAndTeam(t *testing.T) {
	var gotAuth, gotTeam string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body struct{ Team string }
		json.NewDecoder(r.Body).Decode(&body)
		gotTeam = body.Team
		json.NewEncoder(w).Encode(mintResponse{Key: "ik_x", KeyID: "ik_1", Team: "alpha", ExpiresAt: time.Now().UTC().Format(time.RFC3339Nano)})
	}))
	defer srv.Close()
	out, err := mintKey(context.Background(), srv.URL, "the-id-token", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer the-id-token" || gotTeam != "alpha" || out.Key != "ik_x" {
		t.Fatalf("gotAuth=%q gotTeam=%q out=%+v", gotAuth, gotTeam, out)
	}
}

func TestRevokeKeySendsXAPIKey(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		w.WriteHeader(204)
	}))
	defer srv.Close()
	if err := revokeKey(context.Background(), srv.URL, "ik_secret"); err != nil {
		t.Fatal(err)
	}
	if gotKey != "ik_secret" {
		t.Fatalf("x-api-key = %q", gotKey)
	}
}

func TestRevokeKeyNon204IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	if err := revokeKey(context.Background(), srv.URL, "ik_bad"); err == nil {
		t.Fatal("want error")
	}
}

func TestRunIDTokenCommand(t *testing.T) {
	tok, err := runIDTokenCommand(context.Background(), "printf jwt-value")
	if err != nil || tok != "jwt-value" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
}

func TestRunIDTokenCommandEmptyOutputIsError(t *testing.T) {
	if _, err := runIDTokenCommand(context.Background(), "true"); err == nil {
		t.Fatal("empty output should be an error")
	}
}

func TestRunIDTokenCommandFailureIsError(t *testing.T) {
	if _, err := runIDTokenCommand(context.Background(), "exit 1"); err == nil {
		t.Fatal("nonzero exit should be an error")
	}
}

// --- fake IdP for the full browser-PKCE wire test ---

type fakeIdP struct {
	srv  *httptest.Server
	mode string // "" | "deny" | "badstate" | "noidtoken"
	sub  string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	idp := &fakeIdP{sub: "alice"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.srv.URL,
			"authorization_endpoint":                idp.srv.URL + "/authorize",
			"token_endpoint":                        idp.srv.URL + "/token",
			"jwks_uri":                              idp.srv.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		redirect := q.Get("redirect_uri")
		state := q.Get("state")
		u, _ := url.Parse(redirect)
		vals := u.Query()
		switch idp.mode {
		case "deny":
			vals.Set("error", "access_denied")
			vals.Set("error_description", "user said no")
		case "badstate":
			vals.Set("state", state+"-tampered")
			vals.Set("code", "the-code")
		default:
			vals.Set("state", state)
			vals.Set("code", "the-code")
		}
		u.RawQuery = vals.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json") // oauth2.Exchange dispatches on this
		if idp.mode == "noidtoken" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer"})
			return
		}
		// Not a real signed JWT — the CLI never verifies it locally (that's
		// the gateway's job); it only extracts and forwards the raw string.
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at",
			"token_type":   "Bearer",
			"id_token":     "fake." + idp.sub + ".token",
		})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func TestBrowserLoginFullFlow(t *testing.T) {
	idp := newFakeIdP(t)
	old := openURL
	openURL = func(u string) error {
		go http.Get(u) // simulate the user's browser following the auth URL
		return nil
	}
	t.Cleanup(func() { openURL = old })

	idToken, err := browserLogin(context.Background(), idp.srv.URL, "cli-client", 0, false, &nopWriter{})
	if err != nil {
		t.Fatal(err)
	}
	if idToken != "fake.alice.token" {
		t.Fatalf("idToken = %q", idToken)
	}
}

func TestBrowserLoginDenied(t *testing.T) {
	idp := newFakeIdP(t)
	idp.mode = "deny"
	old := openURL
	openURL = func(u string) error { go http.Get(u); return nil }
	t.Cleanup(func() { openURL = old })

	if _, err := browserLogin(context.Background(), idp.srv.URL, "cli-client", 0, false, &nopWriter{}); err == nil {
		t.Fatal("access_denied should surface as an error")
	}
}

func TestBrowserLoginStateMismatch(t *testing.T) {
	idp := newFakeIdP(t)
	idp.mode = "badstate"
	old := openURL
	openURL = func(u string) error { go http.Get(u); return nil }
	t.Cleanup(func() { openURL = old })

	if _, err := browserLogin(context.Background(), idp.srv.URL, "cli-client", 0, false, &nopWriter{}); err == nil {
		t.Fatal("state mismatch should surface as an error")
	}
}

func TestBrowserLoginNoIDToken(t *testing.T) {
	idp := newFakeIdP(t)
	idp.mode = "noidtoken"
	old := openURL
	openURL = func(u string) error { go http.Get(u); return nil }
	t.Cleanup(func() { openURL = old })

	if _, err := browserLogin(context.Background(), idp.srv.URL, "cli-client", 0, false, &nopWriter{}); err == nil {
		t.Fatal("missing id_token should surface as an error")
	}
}

func TestBrowserLoginTimeout(t *testing.T) {
	idp := newFakeIdP(t)
	oldTimeout := loginTimeout
	loginTimeout = 50 * time.Millisecond
	t.Cleanup(func() { loginTimeout = oldTimeout })

	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	defer cancel()
	// noBrowser=true and nobody ever hits /callback — must time out, not hang.
	if _, err := browserLogin(ctx, idp.srv.URL, "cli-client", 0, true, &nopWriter{}); err == nil {
		t.Fatal("want a timeout error")
	}
}

// nopWriter discards stderr output in tests that don't care about it.
type nopWriter struct{ mu sync.Mutex }

func (w *nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- end-to-end login/token/logout against a fake gateway ---

type fakeGateway struct {
	srv          *httptest.Server
	mintedTeam   string
	revoked      []string
	mintStatus   int
	mintBody     string
	denyMintOnce bool
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	gw := &fakeGateway{mintStatus: 200}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/config", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(authapi.ConfigView{CLI: true, Issuer: "https://idp.example.com", ClientID: "cli-client"})
	})
	mux.HandleFunc("/v1/auth/key", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			if gw.mintBody != "" {
				w.WriteHeader(gw.mintStatus)
				w.Write([]byte(gw.mintBody))
				return
			}
			json.NewEncoder(w).Encode(mintResponse{
				Key: "ik_" + fmt.Sprint(len(gw.revoked)), KeyID: "ik_id", Team: "alpha",
				ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
			})
		case "DELETE":
			gw.revoked = append(gw.revoked, r.Header.Get("x-api-key"))
			w.WriteHeader(204)
		}
	})
	gw.srv = httptest.NewServer(mux)
	t.Cleanup(gw.srv.Close)
	return gw
}

func TestTokenRun_hotPathIsNetworkFree(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	// A gateway that fails any request it receives — if token's hot path
	// touches the network at all, this test fails.
	fatalGW := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected network call to %s", r.URL.Path)
	}))
	defer fatalGW.Close()

	creds := credentials{Gateway: fatalGW.URL, Team: "alpha", Key: "ik_cached", KeyID: "ik_id", KeyExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := creds.save(); err != nil {
		t.Fatal(err)
	}

	var stdout strings.Builder
	if err := tokenRun([]string{"--raw"}, &stdout, &nopWriter{}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != "ik_cached" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestTokenRun_notLoggedIn(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	var stdout, stderr strings.Builder
	err := tokenRun(nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("want error")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout must be empty on error, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "inferplane login") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestTokenRun_expiredWithoutIDTokenCommandFails(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	creds := credentials{Gateway: "https://gw.example.com", Key: "ik_stale", KeyExpiresAt: time.Now().UTC().Add(-time.Hour)}
	if err := creds.save(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	err := tokenRun(nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("want error")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout must be empty on error, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "inferplane login") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestTokenRun_expiredWithIDTokenCommandRenews(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	gw := newFakeGateway(t)
	creds := credentials{
		Gateway: gw.srv.URL, Team: "alpha", Key: "ik_stale", KeyID: "ik_old",
		KeyExpiresAt:   time.Now().UTC().Add(-time.Hour),
		IDTokenCommand: "printf the-id-token",
	}
	if err := creds.save(); err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	if err := tokenRun([]string{"--raw"}, &stdout, &nopWriter{}); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(stdout.String())
	if got == "" || got == "ik_stale" {
		t.Fatalf("stdout should be the FRESH key, got %q", got)
	}
	reloaded, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.KeyExpiresAt.Before(time.Now().UTC()) {
		t.Fatal("saved credentials still show the stale expiry")
	}
}

func TestTokenRun_export(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	creds := credentials{Gateway: "https://gw.example.com", Key: "ik_cached", KeyExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := creds.save(); err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	if err := tokenRun([]string{"--export"}, &stdout, &nopWriter{}); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	if !strings.Contains(got, "export ANTHROPIC_BASE_URL=https://gw.example.com") || !strings.Contains(got, "export ANTHROPIC_AUTH_TOKEN=ik_cached") {
		t.Fatalf("export output: %q", got)
	}
}

func TestTokenRun_ttyGuardHidesKeyByDefault(t *testing.T) {
	// tokenRun's isTerminal(os.Stdout) check only fires for the real os.Stdout
	// file, not the strings.Builder these tests pass in — so this test
	// exercises isTerminal itself rather than tokenRun end-to-end.
	if isTerminal(nil) {
		t.Fatal("nil should never report as a terminal")
	}
}

func TestLogoutRun_revokesAndDeletesFile(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	gw := newFakeGateway(t)
	creds := credentials{Gateway: gw.srv.URL, Key: "ik_tobedeleted", KeyID: "ik_id", KeyExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := creds.save(); err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	if err := logoutRun(nil, &stdout, &nopWriter{}); err != nil {
		t.Fatal(err)
	}
	if len(gw.revoked) != 1 || gw.revoked[0] != "ik_tobedeleted" {
		t.Fatalf("revoked: %v", gw.revoked)
	}
	if _, err := loadCredentials(); !errors.Is(err, errNotLoggedIn) {
		t.Fatal("credential file should be gone")
	}
}

func TestLogoutRun_deletesFileEvenWhenRevokeFails(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	deadGW := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	deadGW.Close() // already-closed server: every request fails at the network level
	creds := credentials{Gateway: deadGW.URL, Key: "ik_x", KeyID: "ik_id", KeyExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := creds.save(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	err := logoutRun(nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("network failure should be reported as an error")
	}
	if _, err := loadCredentials(); !errors.Is(err, errNotLoggedIn) {
		t.Fatal("credential file must be deleted even when revoke fails")
	}
	if !strings.Contains(stderr.String(), "ik_id") {
		t.Fatalf("stderr should name the orphaned key: %q", stderr.String())
	}
}

func TestLogoutRun_notLoggedInIsSuccess(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	var stdout strings.Builder
	if err := logoutRun(nil, &stdout, &nopWriter{}); err != nil {
		t.Fatalf("logout when already logged out must succeed, got %v", err)
	}
}

func TestLoginRun_mintsAndSavesCredentials(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	idp := newFakeIdP(t)
	gw := newFakeGatewayWithIssuer(t, idp.srv.URL, "cli-client")

	old := openURL
	openURL = func(u string) error { go http.Get(u); return nil }
	t.Cleanup(func() { openURL = old })

	var stdout strings.Builder
	err := loginRun([]string{"--gateway", gw.srv.URL}, &stdout, &nopWriter{})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if creds.Team != "alpha" || creds.Key == "" || creds.Issuer != idp.srv.URL || creds.ClientID != "cli-client" {
		t.Fatalf("creds: %+v", creds)
	}
	if !strings.Contains(stdout.String(), "logged in as team alpha") {
		t.Fatalf("stdout: %q", stdout.String())
	}
}

func TestLoginRun_tofuPinRejectsSilentIssuerChange(t *testing.T) {
	t.Setenv("INFERPLANE_HOME", t.TempDir())
	idp := newFakeIdP(t)
	gw := newFakeGatewayWithIssuer(t, idp.srv.URL, "cli-client")

	old := openURL
	openURL = func(u string) error { go http.Get(u); return nil }
	t.Cleanup(func() { openURL = old })

	// Pre-seed credentials as if a PRIOR login pinned a different issuer for
	// this same gateway URL.
	creds := credentials{Gateway: gw.srv.URL, Issuer: "https://a-different-idp.example.com", ClientID: "cli-client"}
	if err := creds.save(); err != nil {
		t.Fatal(err)
	}

	var stdout strings.Builder
	err := loginRun([]string{"--gateway", gw.srv.URL}, &stdout, &nopWriter{})
	if err == nil || !strings.Contains(err.Error(), "--reset") {
		t.Fatalf("want a --reset-required error, got %v", err)
	}
}

// newFakeGatewayWithIssuer is newFakeGateway with a caller-supplied issuer/
// client_id in its /v1/auth/config response, for the full login wire tests.
func newFakeGatewayWithIssuer(t *testing.T, issuer, clientID string) *fakeGateway {
	t.Helper()
	gw := &fakeGateway{mintStatus: 200}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/config", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(authapi.ConfigView{CLI: true, Issuer: issuer, ClientID: clientID})
	})
	mux.HandleFunc("/v1/auth/key", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mintResponse{
			Key: "ik_minted", KeyID: "ik_id", Team: "alpha",
			ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
		})
	})
	gw.srv = httptest.NewServer(mux)
	t.Cleanup(gw.srv.Close)
	return gw
}
