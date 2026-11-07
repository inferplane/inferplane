package filter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestControlDefaultPersistenceAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "masking.json")
	c, err := OpenControl(path, false)
	if err != nil {
		t.Fatal(err)
	}
	m := &Masking{Filter: fakeFilter{"controlled"}, Global: true, Control: c}
	if m.Enabled("team") {
		t.Fatal("masking must default off")
	}
	before := c.Snapshot()
	if err := c.Set(true, before.Revision); err != nil || !m.Enabled("team") {
		t.Fatalf("enable failed: %v", err)
	}
	if err := c.Set(false, before.Revision); err == nil || !m.Enabled("team") {
		t.Fatal("stale edit disabled masking")
	}
	reopened, err := OpenControl(path, false)
	if err != nil || !reopened.Snapshot().Enabled {
		t.Fatalf("restart lost administrator choice: %v", err)
	}
	if err := c.Set(false, c.Snapshot().Revision); err != nil || m.Enabled("team") {
		t.Fatalf("disable failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(false, c.Snapshot().Revision); err == nil {
		t.Fatal("external edit overwritten")
	}
}

func TestControlRejectsInvalidOrReadOnlyState(t *testing.T) {
	c, err := OpenControl("", false)
	if err != nil || c.Set(true, c.Snapshot().Revision) == nil {
		t.Fatal("unconfigured persistence accepted runtime edit")
	}
	path := filepath.Join(t.TempDir(), "masking.json")
	for _, raw := range []string{`{}`, `{"enabled":null}`, `{"enabled":"false"}`, `{"enabled":false} {}`, `{"enabled":false,"extra":1}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenControl(path, false); err == nil {
			t.Fatalf("invalid state accepted: %s", raw)
		}
	}
}
