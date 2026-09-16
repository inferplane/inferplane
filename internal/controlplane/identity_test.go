package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferplane/inferplane/internal/identity"
)

func TestRequiredIdentityRejectsLegacyOrConflictingSync(t *testing.T) {
	s, err := NewServer("machine", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := identity.Config{Organization: "acme", Required: true}
	if err := s.SetIdentityConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	for _, fingerprint := range []string{"", "wrong", cfg.Fingerprint()} {
		body, _ := json.Marshal(map[string]any{"dataplane": "node", "identityFingerprint": fingerprint})
		req := httptest.NewRequest("POST", "/v1alpha1/sync", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer machine")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if fingerprint != cfg.Fingerprint() {
			if rec.Code != http.StatusConflict {
				t.Fatalf("incompatible client got %d", rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("compatible client got %d: %s", rec.Code, rec.Body.String())
		}
		var response struct {
			IdentityFingerprint string `json:"identityFingerprint"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.IdentityFingerprint != cfg.Fingerprint() {
			t.Fatal("required identity declaration was not echoed")
		}
	}
}
