package controlplane

// Phase 0b-4: policy writes are role-gated (policy-admin, when role gating
// is configured) and every write records an admin_mutation with actor,
// scope, before/after hashes, and generation.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
)

func newWritableServer(t *testing.T, opts ...Option) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(cpPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer("static-tok", dir, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachPolicyStore(context.Background(), newFakeStore()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

func doPut(t *testing.T, url, bearer, name, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url+"/v1alpha1/policies/"+name, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPolicyWriteRoleGating(t *testing.T) {
	mapping := adminauth.MappingConfig{
		AdminGroups: []string{"grp-any"}, // console access for both identities
		RoleMappings: []adminauth.RoleMapping{
			{Group: "grp-policy", Roles: []string{adminauth.RolePolicyAdmin}},
			{Group: "grp-audit", Roles: []string{adminauth.RoleAuditor}},
		},
	}
	// bearer shape x.y.z routes to the verifier; swap claims per case.
	v := &stubVerifier{claims: adminauth.Claims{Subject: "aud1", Issuer: "https://idp", Groups: []string{"grp-any", "grp-audit"}}}
	_, ts := newWritableServer(t, WithOIDC(v, mapping))

	// An auditor (console access, wrong capability) cannot write policy.
	if got := doPut(t, ts.URL, "x.y.z", "team-a", cpPolicyYAML); got != http.StatusForbidden {
		t.Fatalf("auditor policy write = %d, want 403", got)
	}
	// A policy-admin can.
	v.claims = adminauth.Claims{Subject: "pol1", Issuer: "https://idp", Groups: []string{"grp-any", "grp-policy"}}
	if got := doPut(t, ts.URL, "x.y.z", "team-a", cpPolicyYAML); got != http.StatusNoContent {
		t.Fatalf("policy-admin policy write = %d, want 204", got)
	}
	// The static token is platform-admin break-glass, never role-gated.
	if got := doPut(t, ts.URL, "static-tok", "team-a", cpPolicyYAML); got != http.StatusNoContent {
		t.Fatalf("static-token policy write = %d, want 204", got)
	}
}

func TestPolicyWriteRecordsMutation(t *testing.T) {
	s, ts := newWritableServer(t)
	var sink bytes.Buffer
	s.SetMutationLog(&sink)

	if got := doPut(t, ts.URL, "static-tok", "team-a", cpPolicyYAML); got != http.StatusNoContent {
		t.Fatalf("put = %d", got)
	}
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1alpha1/policies/team-a", nil)
	req.Header.Set("Authorization", "Bearer static-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	lines := strings.Split(strings.TrimSpace(sink.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 mutation records, got %d: %s", len(lines), sink.String())
	}
	var put, del MutationRecord
	if err := json.Unmarshal([]byte(lines[0]), &put); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &del); err != nil {
		t.Fatal(err)
	}
	if put.Event != "admin_mutation" || put.Action != "put" || put.Scope != "team-a" ||
		put.Actor != "static-token" || put.Capability != "policies" {
		t.Fatalf("put record mangled: %+v", put)
	}
	// team-a existed before (file seed) and after: both hashes present, with
	// the resulting generation.
	if put.BeforeSHA256 == "" || put.AfterSHA256 == "" || put.Generation == "" {
		t.Fatalf("put record missing hashes/generation: %+v", put)
	}
	if del.Action != "delete" || del.BeforeSHA256 == "" || del.AfterSHA256 != "" {
		t.Fatalf("delete record must hash only the before state: %+v", del)
	}
}
