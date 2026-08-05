package main

import (
	"net/http"
	"net/http/httptest"
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
