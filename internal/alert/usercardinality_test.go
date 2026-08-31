package alert

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestNotifierHasNoPerUserMap is a structural guard on the Notifier struct
// (requirements §A6): fired and firedKey are unbounded in-memory dedup maps,
// bounded in practice only because teams and key ids are admin-created sets.
// A sibling map keyed on a raw user id (keystore.Principal.Owner — a free
// string or an OIDC sub) has no such bound and leaks memory in a long-running
// process. This test lists the struct's current fields literally and fails on
// any new user/owner-named field, so the invariant reaches whoever trips it.
func TestNotifierHasNoPerUserMap(t *testing.T) {
	// The exact field set Notifier has today (internal/alert/alert.go).
	want := map[string]bool{
		"url":        true,
		"thresholds": true,
		"client":     true,
		"wg":         true,
		"mu":         true,
		"fired":      true,
		"firedKey":   true,
		"recent":     true,
		"now":        true,
	}
	typ := reflect.TypeOf((*Notifier)(nil)).Elem()
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		lower := strings.ToLower(name)
		if strings.Contains(lower, "user") || strings.Contains(lower, "owner") {
			t.Fatalf("Notifier grew field %q: Phase 3 forbids a per-user fired map (unbounded in-memory growth on a raw user id) — if you are adding a user-scoped alert channel, that needs a bounded/evicting structure and its own ADR", name)
		}
		if !want[name] {
			t.Fatalf("Notifier grew an unexpected field %q: if it is any form of per-user dedup state, Phase 3 forbids it (unbounded in-memory growth on a raw user id; a user-scoped alert channel needs a bounded/evicting structure and its own ADR) — otherwise update this guard's field list deliberately", name)
		}
	}
	if typ.NumField() != len(want) {
		t.Fatalf("Notifier has %d fields, this guard lists %d — a field was removed; update the literal field list", typ.NumField(), len(want))
	}
}

// TestNotifierFiredMapsStayBounded exercises the EXISTING re-arm path
// (delete(n.fired, team) / delete(n.firedKey, keyID) in Observe/ObserveKey):
// a subject observed below every threshold leaves no entry behind. This
// documents an existing bound; it does not add a new guarantee.
func TestNotifierFiredMapsStayBounded(t *testing.T) {
	// Ratio 0.1 is below every threshold, so no webhook is ever POSTed and
	// the URL is never dialed.
	n := New("http://127.0.0.1:0/webhook", []float64{0.8, 1.0}, time.Second)

	const subjects = 10_000
	for i := 0; i < subjects; i++ {
		n.Observe(fmt.Sprintf("team-%d", i), 100_000, 1_000_000)
	}
	if got := len(n.fired); got != 0 {
		t.Fatalf("fired map holds %d entries after %d below-threshold teams, want 0 (the delete re-arm path bounds it)", got, subjects)
	}

	for i := 0; i < subjects; i++ {
		n.ObserveKey("team-a", fmt.Sprintf("key-%d", i), 100_000, 1_000_000)
	}
	if got := len(n.firedKey); got != 0 {
		t.Fatalf("firedKey map holds %d entries after %d below-threshold keys, want 0 (the delete re-arm path bounds it)", got, subjects)
	}
}
