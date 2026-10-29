package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/analytics"
	"github.com/inferplane/inferplane/internal/server/configapi"
)

func auditMux(t *testing.T, withAnalytics bool) http.Handler {
	t.Helper()
	v := &fakeVerifier{claims: adminauth.Claims{Subject: "u-alpha", Groups: []string{"team-alpha"}}}
	mapping := adminauth.MappingConfig{GroupMappings: []adminauth.GroupMapping{{Group: "team-alpha", Teams: []string{"alpha"}}}}
	if !withAnalytics {
		return AdminMux(stubStore{}, []string{"admin-tok"}, v, mapping, func() configapi.View { return configapi.View{} },
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	}
	ix, err := analytics.OpenSQLite(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	return AdminMux(stubStore{}, []string{"admin-tok"}, v, mapping, func() configapi.View { return configapi.View{} },
		nil, nil, nil, nil, nil, nil, nil, nil, ix, nil, nil, nil, nil, nil, nil, nil)
}

var auditRoutes = []string{
	"/admin/audit/events",
	"/admin/audit/events/01JC0000000000000000000001",
	"/admin/audit/sessions",
	"/admin/audit/sessions/s1:" + "0123456789abcdef0123456789abcdef",
	"/admin/audit/summary",
}

func doAudit(mux http.Handler, method, path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestAuditConsoleMount_FullAdminOnly(t *testing.T) {
	mux := auditMux(t, true)
	if rec := doAudit(mux, "GET", "/admin/audit/events", "admin-tok"); rec.Code != 200 {
		t.Fatalf("static admin token = %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range auditRoutes {
		// team-mapped (non-admin) OIDC identity: forbidden on every route.
		if rec := doAudit(mux, "GET", p, jwtShaped); rec.Code != 403 {
			t.Errorf("team-mapped GET %s = %d, want 403", p, rec.Code)
		}
		// no credentials: unauthorized.
		if rec := doAudit(mux, "GET", p, ""); rec.Code != 401 {
			t.Errorf("anonymous GET %s = %d, want 401", p, rec.Code)
		}
		// GET only, even for the full admin.
		if rec := doAudit(mux, "POST", p, "admin-tok"); rec.Code != 405 {
			t.Errorf("POST %s = %d, want 405", p, rec.Code)
		}
	}
}

func TestAuditConsoleMount_AbsentWithoutAnalytics(t *testing.T) {
	mux := auditMux(t, false)
	for _, p := range auditRoutes {
		if rec := doAudit(mux, "GET", p, "admin-tok"); rec.Code != 404 {
			t.Errorf("GET %s without analytics = %d, want 404", p, rec.Code)
		}
	}
}
