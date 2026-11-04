package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestChangeIsAppendOnly(t *testing.T) {
	plain := Record{SchemaVersion: 1, Event: "admin_team_upserted", ID: "01J", TS: "t", Instance: "i", Request: RequestRef{Ingress: "admin"}}
	b, err := plain.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"change"`) {
		t.Fatalf("a record without change evidence grew a key: %s", b)
	}
	changed := plain
	changed.Event = "admin_observability_changed"
	changed.Request.Change = &ChangeRef{Target: "observability_limits", Before: "sha256:aa", After: "sha256:bb", Names: []string{"corp"}}
	c, err := changed.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := `"request":{"ingress":"admin","model_requested":"","stream":false,"change":{"target":"observability_limits","before":"sha256:aa","after":"sha256:bb","names":["corp"]}}`
	if !strings.Contains(string(c), want) {
		t.Fatalf("change must close the request object:\n%s", c)
	}
}

func TestMixedVersionChainVerifies_Change(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(out, []byte(preAuthMethodFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileSink(out, true)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter("new-instance", filepath.Join(dir, "wal"), []Sink{fs})
	if err != nil {
		t.Fatal(err)
	}
	sub, method := "admin-sub", "oidc"
	w.Append(Record{SchemaVersion: 1, Event: "admin_observability_changed", ID: "01CHANGE000000000000000000", TS: "2026-10-29T10:00:00Z",
		Principal: PrincipalRef{User: &sub, AuthMethod: &method},
		Request:   RequestRef{Ingress: "admin", Change: &ChangeRef{Target: "observability_rules", Before: "sha256:aa", After: "sha256:bb", Names: []string{"console-eng"}}}})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Verify(bytesReader(raw))
	if err != nil || !res.OK || res.Records != 4 {
		t.Fatalf("mixed chain = %+v, %v; want OK with 4 records", res, err)
	}
	if !containsBytes(raw, []byte(`"change":{"target":"observability_rules","before":"sha256:aa","after":"sha256:bb","names":["console-eng"]}`)) {
		t.Fatalf("change evidence missing:\n%s", raw)
	}
}
