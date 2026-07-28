// Command inferplane login/token/logout implement `inferplane login` (ADR-028):
// a developer authenticates once against the company IdP and gets a
// short-lived gateway virtual key minted and cached automatically, instead of
// copying a long-lived ik_... key by hand. `token` is meant to run as Claude
// Code's apiKeyHelper; `logout` revokes the cached key and clears the file.
// CI/service-account provisioning is unaffected — `inferplane keys create`
// and POST /admin/keys keep working exactly as before.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/inferplane/inferplane/internal/server/authapi"
)

// loginTimeout bounds how long `login` waits for the browser callback.
// renewBefore is the margin token uses to decide a cached key still has
// enough life left to hand to a caller — 5 minutes, not the 60s skew
// internal/adminauth uses for JWT clock skew (a different problem: this
// margin covers a LAPTOP clock that's ahead of the server's, since
// expires_at is enforced server-side in internal/keystore.Resolve).
var (
	loginTimeout = 3 * time.Minute
	renewBefore  = 5 * time.Minute
	openURL      = openBrowser // swapped out in tests
)

const httpClientTimeout = 15 * time.Second

func loginCmd(args []string) error  { return loginRun(args, os.Stdout, os.Stderr) }
func tokenCmd(args []string) error  { return tokenRun(args, os.Stdout, os.Stderr) }
func logoutCmd(args []string) error { return logoutRun(args, os.Stdout, os.Stderr) }

// --- validation helpers (pure, table-tested) ---

// validateEndpointURL requires an absolute https URL, EXCEPT for a loopback
// host (127.0.0.1 / ::1 / localhost) — the exception exists for local dev and
// tests, never for a real deployment. Plaintext http to a non-loopback host
// would ship the ID token and the minted key in the clear (ADR-028 r6).
func validateEndpointURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !isTrustedScheme(u) {
		return "", fmt.Errorf("%q must be https (loopback http is allowed only for local dev/tests)", raw)
	}
	return strings.TrimSuffix(raw, "/"), nil
}

// isTrustedScheme is the one rule every gateway/issuer/endpoint URL in this
// file is checked against: https, or plaintext http to a loopback host (dev
// and tests only — never a real deployment, since that would ship the ID
// token and the minted key in the clear).
func isTrustedScheme(u *url.URL) bool {
	return u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHost(u.Hostname()))
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// sameOriginOrSubdomain reports whether candidate is a trusted-scheme URL
// whose host is issuer's host or a subdomain of it — bounds a compromised or
// misconfigured discovery document's blast radius to the issuer's own domain
// (ADR-028 r6). The loopback exception carries over from isTrustedScheme so
// local dev/tests (a plain httptest.Server IdP) work the same way real
// deployments do, just over http instead of https.
func sameOriginOrSubdomain(issuer, candidate string) bool {
	iu, err1 := url.Parse(issuer)
	cu, err2 := url.Parse(candidate)
	if err1 != nil || err2 != nil || !isTrustedScheme(cu) {
		return false
	}
	return cu.Hostname() == iu.Hostname() || strings.HasSuffix(cu.Hostname(), "."+iu.Hostname())
}

// needsRenewal reports whether a cached key is close enough to expiry that
// token should mint a fresh one rather than hand out the cached value.
func needsRenewal(expiresAt, now time.Time) bool {
	return expiresAt.Sub(now) <= renewBefore
}

// --- HTTP helpers ---

var httpClient = &http.Client{Timeout: httpClientTimeout}

// serverError decodes a gateway `{"error":"..."}` body, if present, so the
// CLI can relay the SERVER's message verbatim (the server already produces
// good text — "entitled to multiple teams; specify --team", etc.) rather
// than inventing a worse one.
func serverError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(b, &body) == nil && body.Error != "" {
		return fmt.Errorf("%s", body.Error)
	}
	return fmt.Errorf("gateway returned %s", resp.Status)
}

func fetchAuthConfig(ctx context.Context, gateway string) (authapi.ConfigView, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", gateway+"/v1/auth/config", nil)
	if err != nil {
		return authapi.ConfigView{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return authapi.ConfigView{}, fmt.Errorf("fetch %s/v1/auth/config: %w", gateway, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return authapi.ConfigView{}, fmt.Errorf("gateway has no CLI-discoverable login config (got %s); pass --issuer and --client-id", resp.Status)
	}
	var cfg authapi.ConfigView
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return authapi.ConfigView{}, fmt.Errorf("decode auth config: %w", err)
	}
	if !cfg.CLI {
		return authapi.ConfigView{}, fmt.Errorf("gateway's oidc.cli_login is not enabled; pass --issuer and --client-id, or ask an operator to enable it")
	}
	return cfg, nil
}

type mintResponse struct {
	Key       string `json:"key"`
	KeyID     string `json:"key_id"`
	Team      string `json:"team"`
	ExpiresAt string `json:"expires_at"`
}

// mintKey trades idToken for a fresh virtual key. team may be empty (the
// server auto-resolves a single entitled team, or errors with a message
// naming the ambiguity — see authapi.pickTeam).
func mintKey(ctx context.Context, gateway, idToken, team string) (mintResponse, error) {
	body, _ := json.Marshal(map[string]string{"team": team})
	req, err := http.NewRequestWithContext(ctx, "POST", gateway+"/v1/auth/key", strings.NewReader(string(body)))
	if err != nil {
		return mintResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+idToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return mintResponse{}, fmt.Errorf("mint key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mintResponse{}, serverError(resp)
	}
	var out mintResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return mintResponse{}, fmt.Errorf("decode mint response: %w", err)
	}
	return out, nil
}

func revokeKey(ctx context.Context, gateway, key string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", gateway+"/v1/auth/key", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return serverError(resp)
	}
	return nil
}

// runIDTokenCommand executes an external command (the kubectl exec-credential
// pattern ADR-004 already endorses) and returns its trimmed stdout as the ID
// token. This is the CLI's ONLY renewal path without a browser — there is no
// IdP refresh token cached locally (ADR-028: a stored refresh token would be
// a stronger, gateway-unrevocable credential than the ik_ key it replaces).
func runIDTokenCommand(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("id-token-command failed: %w", err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", errors.New("id-token-command produced no output")
	}
	return tok, nil
}

// --- login ---

func loginRun(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	gateway := fs.String("gateway", os.Getenv("ANTHROPIC_BASE_URL"), "gateway data-plane base URL (required)")
	team := fs.String("team", "", "team to mint the key for (required only if entitled to more than one)")
	issuer := fs.String("issuer", "", "override: OIDC issuer (skips GET /v1/auth/config discovery)")
	clientID := fs.String("client-id", "", "override: OIDC client_id (skips GET /v1/auth/config discovery)")
	port := fs.Int("port", 0, "loopback callback port (0 = ephemeral; set for an IdP that requires an exact redirect match)")
	noBrowser := fs.Bool("no-browser", false, "print the authorization URL instead of opening a browser")
	idTokenCommand := fs.String("id-token-command", "", "run this command to obtain an ID token instead of the browser PKCE flow (e.g. an existing IdP CLI)")
	reset := fs.Bool("reset", false, "accept a changed issuer/client_id for this gateway (TOFU re-pin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *gateway == "" {
		return errors.New("login: --gateway is required (or set ANTHROPIC_BASE_URL)")
	}
	gw, err := validateEndpointURL(*gateway)
	if err != nil {
		return fmt.Errorf("login: --gateway: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	defer cancel()

	iss, cid := *issuer, *clientID
	if iss == "" || cid == "" {
		cfg, err := fetchAuthConfig(ctx, gw)
		if err != nil {
			return fmt.Errorf("login: %w", err)
		}
		if iss == "" {
			iss = cfg.Issuer
		}
		if cid == "" {
			cid = cfg.ClientID
		}
	}
	if _, err := validateEndpointURL(iss); err != nil {
		return fmt.Errorf("login: issuer: %w", err)
	}

	// TOFU pin: a silent issuer/client_id change for an already-known gateway
	// is refused unless the caller explicitly accepts it. Protects an
	// existing login against a later-compromised or misconfigured gateway
	// redirecting SSO to a different IdP (ADR-028 r6).
	if existing, err := loadCredentials(); err == nil && existing.Gateway == gw && !*reset {
		if existing.Issuer != iss || existing.ClientID != cid {
			return fmt.Errorf("login: gateway %s's OIDC identity changed (issuer/client_id); pass --reset to accept this deliberately", gw)
		}
	}

	var idToken string
	if *idTokenCommand != "" {
		idToken, err = runIDTokenCommand(ctx, *idTokenCommand)
		if err != nil {
			return fmt.Errorf("login: %w", err)
		}
	} else {
		idToken, err = browserLogin(ctx, iss, cid, *port, *noBrowser, stderr)
		if err != nil {
			return fmt.Errorf("login: %w", err)
		}
	}

	minted, err := mintKey(ctx, gw, idToken, *team)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, minted.ExpiresAt)
	if err != nil {
		return fmt.Errorf("login: gateway returned an unparseable expires_at %q: %w", minted.ExpiresAt, err)
	}

	creds := credentials{
		Gateway:        gw,
		Issuer:         iss,
		ClientID:       cid,
		IDTokenCommand: *idTokenCommand,
		Team:           minted.Team,
		Key:            minted.Key,
		KeyID:          minted.KeyID,
		KeyExpiresAt:   expiresAt,
	}
	if err := creds.save(); err != nil {
		return fmt.Errorf("login: save credentials: %w", err)
	}

	fmt.Fprintf(stdout, "logged in as team %s; key %s expires %s\n\n", minted.Team, minted.KeyID, minted.ExpiresAt)
	fmt.Fprintln(stdout, `Claude Code — add to ~/.claude/settings.json (use an ABSOLUTE path to the binary):`)
	fmt.Fprintln(stdout, `  { "apiKeyHelper": "/usr/local/bin/inferplane token",`)
	fmt.Fprintf(stdout, "    \"env\": { \"ANTHROPIC_BASE_URL\": %q, \"CLAUDE_CODE_API_KEY_HELPER_TTL_MS\": \"3600000\" } }\n", gw)
	fmt.Fprintln(stdout, `OpenCode / scripts:  eval "$(inferplane token --export)"`)
	return nil
}

// browserLogin runs the loopback Authorization Code + PKCE flow (RFC 8252)
// and returns the ID token. The CLI is its own OAuth public client — reusing
// the console SPA's client_id is deliberately NOT supported (see
// config.CLILoginConfig's doc comment): the two are validated by DISTINCT
// server-side Verifier instances precisely so this can't happen by accident.
func browserLogin(ctx context.Context, issuer, clientID string, port int, noBrowser bool, stderr io.Writer) (string, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return "", fmt.Errorf("OIDC discovery on %s: %w", issuer, err)
	}
	endpoint := provider.Endpoint()
	for _, ep := range []string{endpoint.AuthURL, endpoint.TokenURL} {
		if !sameOriginOrSubdomain(issuer, ep) {
			return "", fmt.Errorf("discovered endpoint %s is not same-origin with issuer %s", ep, issuer)
		}
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", fmt.Errorf("listen on loopback: %w", err)
	}
	defer ln.Close()
	redirectURI := fmt.Sprintf("http://%s/callback", ln.Addr().String())

	oauthCfg := &oauth2.Config{
		ClientID:    clientID,
		Endpoint:    endpoint,
		RedirectURL: redirectURI,
		Scopes:      []string{oidc.ScopeOpenID},
	}

	verifier := oauth2.GenerateVerifier()
	state, err := randomState()
	if err != nil {
		return "", err
	}
	authURL := oauthCfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))

	fmt.Fprintf(stderr, "Open this URL to sign in (or it will open automatically):\n\n  %s\n\n", authURL)
	if !noBrowser {
		if err := openURL(authURL); err != nil {
			fmt.Fprintf(stderr, "(could not open a browser automatically: %v)\n", err)
		}
	}

	code, err := waitForCallback(ctx, ln, state)
	if err != nil {
		return "", err
	}

	tok, err := oauthCfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", fmt.Errorf("code exchange: %w", err)
	}
	idToken, _ := tok.Extra("id_token").(string)
	if idToken == "" {
		return "", errors.New("IdP returned no id_token (check that the openid scope is granted)")
	}
	return idToken, nil
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// waitForCallback runs a one-shot HTTP server on ln and returns the
// authorization code from the first (and only) /callback request that passes
// validation. Any other path 404s WITHOUT consuming the one-shot, so a
// favicon request can't kill the login. Validation order: state (constant-
// time) before error, error before code — an attacker-supplied `error` must
// never be trusted ahead of proving the state matches.
func waitForCallback(ctx context.Context, ln net.Listener, wantState string) (string, error) {
	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	var once sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotState := q.Get("state")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		switch {
		case subtle.ConstantTimeCompare([]byte(gotState), []byte(wantState)) != 1:
			writeCallbackPage(w, "Login failed", "state mismatch — this callback did not come from the request this CLI made.")
			once.Do(func() { results <- result{err: errors.New("state mismatch")} })
		case q.Get("error") != "":
			msg := q.Get("error") + ": " + q.Get("error_description")
			writeCallbackPage(w, "Login failed", msg)
			once.Do(func() { results <- result{err: fmt.Errorf("authorization denied: %s", msg)} })
		case q.Get("code") == "":
			writeCallbackPage(w, "Login failed", "no authorization code in callback.")
			once.Do(func() { results <- result{err: errors.New("no code in callback")} })
		default:
			writeCallbackPage(w, "Signed in to inferplane", "You can close this tab.")
			once.Do(func() { results <- result{code: q.Get("code")} })
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	select {
	case r := <-results:
		return r.code, r.err
	case <-ctx.Done():
		return "", fmt.Errorf("timed out waiting for the browser callback (%s)", loginTimeout)
	}
}

func writeCallbackPage(w http.ResponseWriter, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// msg can carry IdP-supplied query params (error/error_description) — escape
	// both so a metacharacter in them can never become markup.
	fmt.Fprintf(w, "<!doctype html><html><body><h3>%s</h3><p>%s</p></body></html>",
		html.EscapeString(title), html.EscapeString(msg))
}

// openBrowser shells out to the platform opener. Failure is never fatal — the
// URL is already on stderr and the loopback listener is waiting regardless.
func openBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}

// --- token ---

func tokenRun(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	export := fs.Bool("export", false, "print `export ANTHROPIC_BASE_URL=... ANTHROPIC_AUTH_TOKEN=...` instead of the bare key")
	raw := fs.Bool("raw", false, "print the key even on a terminal (default: terminal output shows only key_id + expiry, to avoid the key landing in scrollback/pasted tickets)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	creds, err := loadCredentials()
	if err != nil {
		fmt.Fprintln(stderr, "inferplane:", err)
		return err
	}

	if needsRenewal(creds.KeyExpiresAt, time.Now().UTC()) {
		if creds.IDTokenCommand == "" {
			fmt.Fprintln(stderr, "inferplane: session expired; run: inferplane login")
			return errors.New("session expired")
		}
		ctx, cancel := context.WithTimeout(context.Background(), httpClientTimeout)
		defer cancel()
		idToken, err := runIDTokenCommand(ctx, creds.IDTokenCommand)
		if err != nil {
			fmt.Fprintln(stderr, "inferplane:", err)
			return err
		}
		minted, err := mintKey(ctx, creds.Gateway, idToken, creds.Team)
		if err != nil {
			fmt.Fprintln(stderr, "inferplane: renew failed:", err)
			return err
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, minted.ExpiresAt)
		if err != nil {
			fmt.Fprintln(stderr, "inferplane: renew: unparseable expires_at from gateway")
			return err
		}
		creds.Key, creds.KeyID, creds.KeyExpiresAt, creds.Team = minted.Key, minted.KeyID, expiresAt, minted.Team
		if err := creds.save(); err != nil {
			fmt.Fprintln(stderr, "inferplane: renew: save credentials:", err)
			return err
		}
	}

	if *export {
		fmt.Fprintf(stdout, "export ANTHROPIC_BASE_URL=%s\n", creds.Gateway)
		fmt.Fprintf(stdout, "export ANTHROPIC_AUTH_TOKEN=%s\n", creds.Key)
		return nil
	}
	if !*raw && isTerminal(os.Stdout) {
		fmt.Fprintf(stdout, "%s (expires %s) — pass --raw or pipe stdout to print the key itself\n", creds.KeyID, creds.KeyExpiresAt.Format(time.RFC3339))
		return nil
	}
	fmt.Fprintln(stdout, creds.Key)
	return nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// --- logout ---

func logoutRun(args []string, stdout, stderr io.Writer) error {
	creds, err := loadCredentials()
	if err != nil {
		fmt.Fprintln(stdout, "not logged in")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpClientTimeout)
	defer cancel()
	revokeErr := revokeKey(ctx, creds.Gateway, creds.Key)

	// The local secret is deleted UNCONDITIONALLY — a network failure must
	// never block clearing local state, which is the security-relevant half
	// of logout (ADR-028).
	if err := deleteCredentials(); err != nil {
		fmt.Fprintln(stderr, "inferplane: could not remove credentials file:", err)
		return err
	}
	if revokeErr != nil {
		fmt.Fprintf(stderr, "inferplane: warning: could not revoke %s (%v); it expires at %s\n", creds.KeyID, revokeErr, creds.KeyExpiresAt.Format(time.RFC3339))
		fmt.Fprintf(stderr, "  an operator can revoke it directly: inferplane keys revoke --id %s --store <path>\n", creds.KeyID)
		return revokeErr
	}
	fmt.Fprintln(stdout, "logged out")
	return nil
}
