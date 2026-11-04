package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
)

// Review Focus 5: every route, reads included, is full-admin only; a mapped
// member gets 403 and an admin_denied record; anything else falls through.
func TestFullAdminRoutesGateEveryRoute(t *testing.T) {
	var reached []string
	record := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = append(reached, r.Method+" "+r.URL.Path) })
	routes := map[string]http.Handler{
		"GET /admin/observability":        record,
		"PUT /admin/observability/limits": record,
		"PUT /admin/observability/rules":  record,
	}
	adminCalls := 0
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { adminCalls++; http.NotFound(w, r) })
	var lines bytes.Buffer
	aud, err := audit.NewWriter("routes-test", filepath.Join(t.TempDir(), "wal"), []audit.Sink{audit.NewWriterSink("lines", &lines, true)})
	if err != nil {
		t.Fatal(err)
	}
	verifier := &fakeVerifier{claims: adminauth.Claims{Subject: "fixture-member", Groups: []string{"members"}}}
	mapping := adminauth.MappingConfig{GroupMappings: []adminauth.GroupMapping{{Group: "members", Teams: []string{"fixture"}}}}
	mux := WithFullAdminRoutes(admin, routes, []string{"fixture-admin"}, verifier, mapping, aud)
	do := func(method, path, bearer string) int {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	for pattern := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		if got := do(method, path, ""); got != http.StatusUnauthorized {
			t.Errorf("%s without a credential = %d, want 401", pattern, got)
		}
		if got := do(method, path, jwtShaped); got != http.StatusForbidden {
			t.Errorf("%s as a team-mapped member = %d, want 403", pattern, got)
		}
		if got := do(method, path, "fixture-admin"); got != http.StatusOK {
			t.Errorf("%s as full admin = %d, want 200", pattern, got)
		}
	}
	if len(reached) != len(routes) {
		t.Fatalf("handlers reached = %v, want only the full-admin calls", reached)
	}
	if got := do("GET", "/admin/keys", "fixture-admin"); got != http.StatusNotFound || adminCalls != 1 {
		t.Fatalf("other admin paths must fall through: status %d, admin calls %d", got, adminCalls)
	}
	if got := do("POST", "/admin/observability", "fixture-admin"); got != http.StatusNotFound || adminCalls != 2 {
		t.Fatalf("an unregistered method must fall through: status %d, admin calls %d", got, adminCalls)
	}
	if err := aud.Close(); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(lines.String(), `"event":"admin_denied"`); n != len(routes) {
		t.Fatalf("admin_denied records = %d, want %d", n, len(routes))
	}
}

func TestFullAdminRoutesWithoutRoutesIsTheAdminMux(t *testing.T) {
	calls := 0
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ })
	h := WithFullAdminRoutes(admin, nil, nil, nil, adminauth.MappingConfig{}, nil)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/admin/observability", nil))
	if calls != 1 {
		t.Fatal("no routes must leave the admin handler in place")
	}
}
