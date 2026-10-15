package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/identity"
)

func TestPolicyListDeclaredAccountBindings(t *testing.T) {
	s, err := NewServer("read-token", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	person, err := identity.NewHuman("org", "https://private-issuer.example", "private-subject")
	if err != nil {
		t.Fatal(err)
	}
	cfg := identity.Config{Organization: "org", Required: true, Bindings: []identity.Binding{
		{Identity: person, AccountRef: " legacy-account "},
	}}
	if err := s.SetIdentityConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	for _, token := range []string{"", "wrong", "read-token"} {
		req := httptest.NewRequest("GET", "/v1alpha1/policies", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if token != "read-token" {
			if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "legacy-account") {
				t.Fatalf("unauthorized binding read: %d %s", rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("binding read: %d", rec.Code)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("declared financial bindings must not be cached")
		}
		var body struct {
			Bindings []struct {
				ReportedRef string `json:"reported_ref"`
				AccountRef  string `json:"account_ref"`
				Kind        string `json:"kind"`
			} `json:"identity_bindings"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Bindings) != 1 || body.Bindings[0].ReportedRef != person.CanonicalRef() ||
			body.Bindings[0].AccountRef != " legacy-account " || body.Bindings[0].Kind != "human" {
			t.Fatalf("wrong account correlation: %+v", body.Bindings)
		}
		for _, forbidden := range []string{"private-subject", "private-issuer.example", `"issuer"`, `"subject"`, `"groups"`} {
			if strings.Contains(rec.Body.String(), forbidden) {
				t.Fatalf("raw identity field escaped the binding DTO: %s", forbidden)
			}
		}
	}
}

func TestPolicyListDoesNotInventAccountBindings(t *testing.T) {
	s, err := NewServer("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handlePolicyList(rec, httptest.NewRequest("GET", "/v1alpha1/policies", nil))
	if strings.Contains(rec.Body.String(), "identity_bindings") {
		t.Fatalf("unconfigured identity gained guessed bindings: %s", rec.Body.String())
	}
}
