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
		if err := run(listen, "", ""); err == nil || !strings.Contains(err.Error(), "INFERPLANED_TOKEN") {
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
