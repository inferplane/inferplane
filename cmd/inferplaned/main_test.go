package main

import (
	"strings"
	"testing"
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
