package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/server/configapi"
	"github.com/inferplane/inferplane/internal/server/debugapi"
)

// GET /admin/debug/governance (roadmap ④ remote half): admin-auth gated,
// secret-free snapshot; absent closure = route not mounted.
func TestDebugGovernanceRoute(t *testing.T) {
	snap := func() debugapi.Snapshot {
		return debugapi.Snapshot{
			PolicySource: "control_plane",
			Teams: map[string]debugapi.Team{
				"alpha": {
					Leases: []debugapi.Lease{{Period: "CalendarMonth", AllowanceUSDMicros: 10_000, ExpiresAt: time.Now().Add(15 * time.Second), HardCap: true}},
					Share:  &debugapi.Share{RPM: 150},
				},
			},
		}
	}
	mux := AdminMux(stubStore{}, []string{"admin-tok"}, nil, adminauth.MappingConfig{}, func() configapi.View { return configapi.View{} }, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, snap)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/debug/governance", nil))
	if rec.Code != 401 {
		t.Fatalf("no token: status %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/debug/governance", nil)
	req.Header.Set("Authorization", "Bearer admin-tok")
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"control_plane"`, `"alpha"`, `"allowance_usd_micros":10000`, `"rpm":150`} {
		if !strings.Contains(body, want) {
			t.Fatalf("snapshot missing %s: %s", want, body)
		}
	}

	// nil closure → route not mounted at all.
	mux = AdminMux(stubStore{}, []string{"admin-tok"}, nil, adminauth.MappingConfig{}, func() configapi.View { return configapi.View{} }, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/admin/debug/governance", nil)
	req.Header.Set("Authorization", "Bearer admin-tok")
	mux.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("nil closure: status %d, want 404", rec.Code)
	}
}
