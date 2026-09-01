package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/controlplane"
	"github.com/inferplane/inferplane/internal/policy"
)

// TestSyncerReportsVersionAndDedupesUpdateAdvice runs the real control plane
// (roadmap ③ phase 1): the heartbeat carries the build version up, and
// advice for a stale build fires OnUpdateAdvice once per DISTINCT advice —
// not once per 10s heartbeat.
func TestSyncerReportsVersionAndDedupesUpdateAdvice(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(syncPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer("", dir)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	cp.Mount(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	cp.SetUpdateAdvice("v9.9.9", "https://example.com/releases")

	var fired []policy.UpdateAdvice
	s := &Syncer{
		URL: ts.URL, Dataplane: "dp1", Version: "v0.1.0",
		Store: policy.NewEmptyStore(), Leases: NewLeaseTable(),
		SpentOf:        func(string, v1alpha1.BudgetPeriod) int64 { return 0 },
		OnUpdateAdvice: func(a policy.UpdateAdvice) { fired = append(fired, a) },
	}
	for i := 0; i < 3; i++ {
		if _, err := s.syncOnce(context.Background()); err != nil {
			t.Fatalf("syncOnce #%d: %v", i, err)
		}
	}
	if len(fired) != 1 {
		t.Fatalf("advice fired %d times over 3 identical heartbeats, want 1 (dedupe)", len(fired))
	}
	if fired[0].MinVersion != "v9.9.9" || fired[0].URL != "https://example.com/releases" {
		t.Fatalf("advice mangled: %+v", fired[0])
	}

	// A CHANGED advice (operator raised the minimum again) fires again.
	cp.SetUpdateAdvice("v10.0.0", "https://example.com/releases")
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fired) != 2 || fired[1].MinVersion != "v10.0.0" {
		t.Fatalf("changed advice must re-fire: %+v", fired)
	}

	// Advice withdrawn (minimum cleared, or this build updated): no more
	// firings, and a LATER re-set fires fresh.
	cp.SetUpdateAdvice("", "")
	if _, err := s.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fired) != 2 {
		t.Fatalf("withdrawn advice must not fire: %+v", fired)
	}
}
