package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

func TestAuthConfigHandlerUnconfigured(t *testing.T) {
	h := AuthConfigHandler(func() *AuthConfigView { return nil })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/auth/config", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("nil view must 404, got %d", w.Code)
	}
}

func TestAuthConfigHandlerExactKeySet(t *testing.T) {
	h := AuthConfigHandler(func() *AuthConfigView {
		return &AuthConfigView{SSO: true, Issuer: "https://idp.example.com", ClientID: "client-1"}
	})
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/auth/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("configured view must 200, got %d", w.Code)
	}

	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"client_id", "issuer", "sso"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v (secret-free — no other field must ever leak)", keys, want)
	}
	for i, k := range keys {
		if k != want[i] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
}

func TestAuthConfigHandlerNoSSOOmitsIssuer(t *testing.T) {
	h := AuthConfigHandler(func() *AuthConfigView { return &AuthConfigView{SSO: false} })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/auth/config", nil))

	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["issuer"]; ok {
		t.Fatalf("empty issuer must be omitted, not sent as empty string: %v", m)
	}
	if _, ok := m["client_id"]; ok {
		t.Fatalf("empty client_id must be omitted: %v", m)
	}
}
