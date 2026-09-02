package server

// Phase 0b-3 duty separation: with RoleMappings configured, each management
// route class requires a capability; without them, authority is unchanged
// (that half is proven by every pre-existing test in this package running
// against a config with no RoleMappings).

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/server/configapi"
)

func roleGatedMux(t *testing.T, groups []string) *httptest.Server {
	t.Helper()
	v := &fakeVerifier{claims: adminauth.Claims{Subject: "u1", Issuer: "https://idp.example", Groups: groups}}
	mapping := adminauth.MappingConfig{
		AdminGroups: []string{"grp-platform"},
		GroupMappings: []adminauth.GroupMapping{
			{Group: "grp-alpha", Teams: []string{"alpha"}},
		},
		RoleMappings: []adminauth.RoleMapping{
			{Group: "grp-platform", Roles: []string{adminauth.RolePlatformAdmin}},
			{Group: "grp-audit", Roles: []string{adminauth.RoleAuditor}},
			{Group: "grp-alpha", Roles: []string{adminauth.RoleTeamAdmin}},
		},
	}
	mux := AdminMux(stubStore{}, []string{"admin-tok"}, v, mapping, func() configapi.View { return configapi.View{} }, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func doAs(t *testing.T, ts *httptest.Server, method, path string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	req.Header.Set("Authorization", "Bearer x.y.z") // OIDC-shaped → fakeVerifier
	req.Header.Set("Content-Type", "application/json")
	ts.Config.Handler.ServeHTTP(rec, req)
	return rec.Code
}

func TestRoleGatingDeniesOutsideCapability(t *testing.T) {
	// An auditor (no team mapping at all — legitimate under role gating)
	// can read the audit surface but cannot touch keys.
	ts := roleGatedMux(t, []string{"grp-audit"})
	if got := doAs(t, ts, "GET", "/admin/audit/verify"); got == 401 || got == 403 {
		t.Fatalf("auditor must reach audit verify, got %d", got)
	}
	if got := doAs(t, ts, "POST", "/admin/keys"); got != 403 {
		t.Fatalf("auditor must not issue keys, got %d", got)
	}

	// A team-admin can reach keys but not the audit surface.
	ts = roleGatedMux(t, []string{"grp-alpha"})
	if got := doAs(t, ts, "GET", "/admin/audit/verify"); got != 403 {
		t.Fatalf("team-admin must not read audit, got %d", got)
	}
	if got := doAs(t, ts, "POST", "/admin/keys"); got == 401 || got == 403 {
		t.Fatalf("team-admin must reach keys, got %d", got)
	}
}

func TestRoleGatingPlatformAdminAndBreakGlassKeepEverything(t *testing.T) {
	ts := roleGatedMux(t, []string{"grp-platform"})
	for _, p := range []struct{ method, path string }{
		{"GET", "/admin/audit/verify"}, {"POST", "/admin/keys"},
	} {
		if got := doAs(t, ts, p.method, p.path); got == 401 || got == 403 {
			t.Fatalf("platform-admin denied on %s %s: %d", p.method, p.path, got)
		}
	}

	// Break-glass (static token) is platform-admin by definition.
	req := httptest.NewRequest("GET", "/admin/audit/verify", nil)
	req.Header.Set("Authorization", "Bearer admin-tok")
	rec := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(rec, req)
	if rec.Code == 401 || rec.Code == 403 {
		t.Fatalf("break-glass denied under role gating: %d", rec.Code)
	}
}
