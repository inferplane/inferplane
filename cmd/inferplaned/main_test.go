package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/controlplane"
	"github.com/inferplane/inferplane/internal/telemetry"
)

// PR #50 review finding (HIGH): serving unauthenticated beyond loopback must
// be refused at startup, not just logged.
func TestRunRefusesUnauthenticatedNonLoopback(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:7601", ":7601", "10.0.0.5:7601", "[::]:7601"} {
		if err := run(listen, "", "", nil); err == nil || !strings.Contains(err.Error(), "INFERPLANED_TOKEN") {
			t.Fatalf("run(%q) without token: err = %v, want refusal", listen, err)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:7601":   true,
		"localhost:7601":   true,
		"[::1]:7601":       true,
		"127.8.9.1:7601":   true,
		"0.0.0.0:7601":     false,
		":7601":            false,
		"[::]:7601":        false,
		"10.0.0.5:7601":    false,
		"example.com:7601": false, // unresolved hostnames are not trusted
		"not-an-address":   false,
	}
	for listen, want := range cases {
		if got := isLoopback(listen); got != want {
			t.Fatalf("isLoopback(%q) = %v, want %v", listen, got, want)
		}
	}
}

// ADR-036: usage telemetry must be available WITHOUT --policies — a
// telemetry-only inferplaned is a valid deployment. (run() wires it
// unconditionally; this pins the mux-level behavior via a quick boot check
// against the mounted handler set.)
func TestUsageMountedWithoutPolicies(t *testing.T) {
	// run() blocks serving; test the wiring the same way it is built.
	mux := http.NewServeMux()
	agg := telemetry.NewMemoryAggregator(24 * time.Hour)
	controlplane.NewUsageServer("", agg).Mount(mux)
	req := httptest.NewRequest("GET", "/v1alpha1/usage", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("usage query without policy server must work, got %d", rec.Code)
	}
}

// A JWT-shaped INFERPLANED_TOKEN would be routed to the OIDC verifier by
// authn's total rule and could never authenticate — reject it at boot
// rather than let an operator discover a permanently-broken static token.
func TestValidateBootRejectsJWTShapedToken(t *testing.T) {
	shaped := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1In0.sig"
	if err := validateBoot("127.0.0.1:7601", shaped, nil); err == nil || !strings.Contains(err.Error(), "JWT-shaped") {
		t.Fatalf("validateBoot with a JWT-shaped token: err = %v, want rejection", err)
	}
}

// An SSO-only deploy (empty static token, OIDC configured) is a legitimate
// non-loopback posture — OIDC covers authentication on its own.
func TestValidateBootAllowsSSOOnlyNonLoopback(t *testing.T) {
	o := &oidcEnv{Issuer: "https://idp.example.com", ClientID: "client-1", GroupsClaim: "groups", AllowedGroups: []string{"ops"}}
	if err := validateBoot("0.0.0.0:7601", "", o); err != nil {
		t.Fatalf("SSO-only non-loopback deploy must be allowed, got: %v", err)
	}
	// Without OIDC and without a token, the same address is still refused.
	if err := validateBoot("0.0.0.0:7601", "", nil); err == nil {
		t.Fatal("unauthenticated non-loopback must still be refused")
	}
}

// ADR-038: INFERPLANED_POLICY_DSN makes the database authoritative and the
// --policies file channel the one-time seed source — a DSN with nothing to
// seed from must refuse to boot. Pure env/flag logic: no database needed.
func TestBuildMuxRejectsPolicyDSNWithoutPolicies(t *testing.T) {
	t.Setenv("INFERPLANED_POLICY_DSN", "postgres://ip:secretpw@db.example.com:5432/inferplane")
	_, _, closePG, err := buildMux("", "", nil)
	if closePG != nil {
		defer closePG()
	}
	if err == nil || !strings.Contains(err.Error(), "--policies") {
		t.Fatalf("buildMux with INFERPLANED_POLICY_DSN and no --policies: err = %v, want error mentioning --policies", err)
	}
}

// The unset-DSN behavior contract: file-authoritative, no store attached,
// policy reads served, policy writes 405 (the console's read-only signal).
func TestBuildMuxWithoutPolicyDSNIsFileAuthoritative(t *testing.T) {
	dir := t.TempDir()
	doc := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: t5
spec:
  subject:
    team: alpha
  rules:
  - name: models
    failurePolicy: FailOpen
    modelAccess:
      allow: ["*"]
`
	if err := os.WriteFile(filepath.Join(dir, "t5.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	mux, cp, closePG, err := buildMux(dir, "", nil)
	if closePG != nil {
		defer closePG()
	}
	if err != nil {
		t.Fatalf("buildMux without INFERPLANED_POLICY_DSN: %v", err)
	}
	if cp == nil {
		t.Fatal("buildMux with --policies must return a control-plane server")
	}
	if cp.PolicyStoreAttached() {
		t.Fatal("no DSN set: PolicyStoreAttached() must be false")
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1alpha1/policies", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1alpha1/policies = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/v1alpha1/policies/x", strings.NewReader("{}")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /v1alpha1/policies/x without a store = %d, want 405", rec.Code)
	}
}
