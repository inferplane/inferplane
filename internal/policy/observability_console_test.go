package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

func TestMergeObservabilityMatchesStoreAndReportsConflict(t *testing.T) {
	s := NewEmptyStore()
	if rej := s.ApplyWire([]v1alpha1.GovernancePolicy{
		obsDoc("team", "eng", "", &v1alpha1.ObservabilityRule{Content: &v1alpha1.SignalExport{Exporter: "vault"}}),
		obsDoc("bob", "", "bob", &v1alpha1.ObservabilityRule{Content: &v1alpha1.SignalExport{Exporter: "other-vault"}}),
	}); len(rej) != 0 {
		t.Fatalf("rejections = %+v", rej)
	}
	bob := s.Observability("eng", "bob")
	if bob.Content != "" || !bob.ContentConflict {
		t.Fatalf("bob = %+v: disagreeing content exporters must disable content and say why", bob)
	}
	if alice := s.Observability("eng", "alice"); alice.Content != "vault" || alice.ContentConflict {
		t.Fatalf("alice = %+v", alice)
	}
	merged := MergeObservability(s.Policies(), "eng", "bob")
	if merged.Content != bob.Content || merged.ContentConflict != bob.ContentConflict {
		t.Fatalf("MergeObservability = %+v, store = %+v", merged, bob)
	}
	if none := MergeObservability(nil, "eng", ""); none.ContentConflict || len(none.Audit) != 0 || len(none.Traces) != 0 {
		t.Fatalf("empty set = %+v", none)
	}
}

func TestCheckLocalValidatesWithoutInstalling(t *testing.T) {
	body, err := json.Marshal(obsDoc("ok", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(writePolicy(t, t.TempDir(), "ok.yaml", string(body)))
	if err != nil {
		t.Fatal(err)
	}
	s.SetObservabilityCeiling(func(exporter, signal string) error {
		if exporter == "corp" {
			return nil
		}
		return fmt.Errorf("exporter %q is not declared", exporter)
	})
	bad := obsDoc("bad", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "missing"}})
	p, err := FromV1Alpha1(&bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckLocal(append(s.Policies(), p)); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("CheckLocal = %v, want the ceiling refusal naming the exporter", err)
	}
	if err := s.CheckLocal(append(s.Policies(), s.Policies()[0])); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("CheckLocal duplicate = %v", err)
	}
	if got := s.Policies(); len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("CheckLocal installed something: %+v", got)
	}
}

// A write that lands while Reload is reading must not be recorded as loaded:
// Reload records mtimes before it reads, so the next Watch poll reloads it.
func TestReloadCapturesMtimesBeforeReading(t *testing.T) {
	first, _ := json.Marshal(obsDoc("first", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}))
	second, _ := json.Marshal(obsDoc("second", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}))
	path := writePolicy(t, t.TempDir(), "obs.yaml", string(first))
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { afterPolicyRead = nil })
	afterPolicyRead = func() {
		afterPolicyRead = nil
		if err := os.WriteFile(path, second, 0o600); err != nil {
			t.Error(err)
		}
		later := time.Now().Add(2 * time.Second)
		if err := os.Chtimes(path, later, later); err != nil {
			t.Error(err)
		}
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := s.Policies()[0].Name; got != "first" {
		t.Fatalf("the seam runs after the read; snapshot = %q", got)
	}
	if !s.changed() {
		t.Fatal("a write during the read was recorded as loaded; the Watch would never pick it up")
	}
}

// Two Reloads must not interleave: the console writes its file and reloads
// while the Watch may be mid-read of the old bytes. The later reload wins.
//
// Ordering is deterministic, no sleeps: the first Reload parks in the
// afterPolicyRead seam, which runs while Reload holds reloadMu, so TryLock
// proves the mutex is held at that moment. Without the mutex the second
// Reload would install the new bytes and the parked stale one would then
// overwrite them.
func TestConcurrentReloadKeepsTheLaterWrite(t *testing.T) {
	old, _ := json.Marshal(obsDoc("old", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}))
	next, _ := json.Marshal(obsDoc("new", "eng", "", &v1alpha1.ObservabilityRule{Audit: &v1alpha1.SignalExport{Exporter: "corp"}}))
	path := writePolicy(t, t.TempDir(), "obs.yaml", string(old))
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	read, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	t.Cleanup(func() { afterPolicyRead = nil })
	afterPolicyRead = func() {
		if calls.Add(1) == 1 { // only the first (stale) reader waits; later reloads pass straight through
			close(read)
			<-release
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = s.Reload() }() // reads the old bytes, then waits
	<-read
	if s.reloadMu.TryLock() {
		s.reloadMu.Unlock()
		close(release)
		wg.Done() // the second reload never starts on this path
		wg.Wait()
		t.Fatal("Reload does not hold reloadMu while it reads: a concurrent reload could interleave")
	}
	if err := os.WriteFile(path, next, 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	go func() { defer wg.Done(); _ = s.Reload() }() // the console's reload after its write
	close(release)
	wg.Wait()
	if got := s.Policies()[0].Name; got != "new" {
		t.Fatalf("snapshot = %q: the stale reload installed after the newer one", got)
	}
}
