package controlplane

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/policy"
)

const exportFixture = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: demo-team
spec:
  subject:
    team: demo
  rules:
  - name: monthly-cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 50000
      hardCap: true
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: demo-models
spec:
  subject:
    team: demo
  rules:
  - name: models
    failurePolicy: FailOpen
    modelAccess:
      allow: ["*"]
`

// Export → parse back through the SAME loader → semantically equal round
// trip: the export IS a valid --policies input on another server.
func TestConfigExportRoundTrips(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(src, []byte(exportFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer("tok", src)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Mount(mux)

	req := httptest.NewRequest("GET", "/v1alpha1/config/export", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "yaml") {
		t.Fatalf("content type: %q", rec.Header().Get("Content-Type"))
	}

	out := filepath.Join(dir, "exported.yaml")
	if err := os.WriteFile(out, rec.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	orig, _, err := policy.LoadWirePaths(src)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := policy.LoadWirePaths(out)
	if err != nil {
		t.Fatalf("exported YAML does not load back through the policy loader: %v\n%s", err, rec.Body.String())
	}
	if !reflect.DeepEqual(orig, back) {
		t.Fatalf("round trip not semantically equal:\norig: %+v\nback: %+v", orig, back)
	}
}

func TestConfigExport401(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(src, []byte(exportFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer("tok", src)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Mount(mux)
	req := httptest.NewRequest("GET", "/v1alpha1/config/export", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("export without token must 401, got %d", rec.Code)
	}
}
