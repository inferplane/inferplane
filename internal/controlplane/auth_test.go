package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
)

// stubVerifier records every call so the anti-bypass test can assert zero
// calls when the bearer isn't JWT-shaped.
type stubVerifier struct {
	calls  int
	claims adminauth.Claims
	err    error
}

func (s *stubVerifier) Verify(context.Context, string) (adminauth.Claims, error) {
	s.calls++
	return s.claims, s.err
}

func serve(t *testing.T, h http.HandlerFunc, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/x", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func TestAuthnNoAuthConfigured(t *testing.T) {
	h := authn("", authOptions{}, ok)
	w := serve(t, h, "")
	if w.Code != http.StatusOK {
		t.Fatalf("no token + no verifier must pass through unauthenticated, got %d", w.Code)
	}
}

func TestAuthnStaticTokenOnly(t *testing.T) {
	h := authn("secret", authOptions{}, ok)
	if w := serve(t, h, "secret"); w.Code != http.StatusOK {
		t.Fatalf("correct static token must pass, got %d", w.Code)
	}
	if w := serve(t, h, "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong static token must 401, got %d", w.Code)
	}
	if w := serve(t, h, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("absent bearer must 401, got %d", w.Code)
	}
}

// TestAuthnAntiBypass is the load-bearing case: a non-JWT-shaped bearer with
// a verifier configured must NEVER reach the verifier — it must fall
// straight to the static-token comparison. A verifier call here would mean
// an attacker could probe the OIDC path with garbage tokens, or worse, that
// routing could be confused into skipping the static check.
func TestAuthnAntiBypass(t *testing.T) {
	v := &stubVerifier{claims: adminauth.Claims{Subject: "u", Groups: []string{"ops"}}}
	mapping := adminauth.MappingConfig{AdminGroups: []string{"ops"}}
	h := authn("secret", authOptions{verifier: v, mapping: mapping}, ok)

	if w := serve(t, h, "not-a-jwt"); w.Code != http.StatusUnauthorized {
		t.Fatalf("non-shaped bearer with wrong static token must 401, got %d", w.Code)
	}
	if v.calls != 0 {
		t.Fatalf("non-shaped bearer must never reach the OIDC verifier, got %d calls", v.calls)
	}

	// The static path must still work unaffected by the verifier being set.
	if w := serve(t, h, "secret"); w.Code != http.StatusOK {
		t.Fatalf("static token must still work with a verifier configured, got %d", w.Code)
	}
	if v.calls != 0 {
		t.Fatalf("static-token success must never touch the verifier, got %d calls", v.calls)
	}
}

func TestAuthnOIDCPath(t *testing.T) {
	shaped := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1In0.sig" // 3 dot-separated base64url segments
	mapping := adminauth.MappingConfig{AdminGroups: []string{"ops"}}

	t.Run("allowed group", func(t *testing.T) {
		v := &stubVerifier{claims: adminauth.Claims{Subject: "u", Groups: []string{"ops"}}}
		h := authn("secret", authOptions{verifier: v, mapping: mapping}, ok)
		w := serve(t, h, shaped)
		if w.Code != http.StatusOK {
			t.Fatalf("allowed group must 200, got %d", w.Code)
		}
		if v.calls != 1 {
			t.Fatalf("shaped bearer must call the verifier exactly once, got %d", v.calls)
		}
	})

	t.Run("unrelated group", func(t *testing.T) {
		v := &stubVerifier{claims: adminauth.Claims{Subject: "u", Groups: []string{"finance"}}}
		h := authn("secret", authOptions{verifier: v, mapping: mapping}, ok)
		w := serve(t, h, shaped)
		if w.Code != http.StatusForbidden {
			t.Fatalf("unrelated group must 403, got %d", w.Code)
		}
	})

	t.Run("verify error", func(t *testing.T) {
		v := &stubVerifier{err: errors.New("expired")}
		h := authn("secret", authOptions{verifier: v, mapping: mapping}, ok)
		w := serve(t, h, shaped)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("verify failure must 401, got %d", w.Code)
		}
	})
}

func TestAuthnSSOOnlyDeploy(t *testing.T) {
	// Empty static token, verifier configured: a legitimate posture (D3) —
	// every request needs OIDC, no break-glass token exists to fall back to.
	v := &stubVerifier{claims: adminauth.Claims{Subject: "u", Groups: []string{"ops"}}}
	mapping := adminauth.MappingConfig{AdminGroups: []string{"ops"}}
	h := authn("", authOptions{verifier: v, mapping: mapping}, ok)

	shaped := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1In0.sig"
	if w := serve(t, h, shaped); w.Code != http.StatusOK {
		t.Fatalf("OIDC must work with no static token configured, got %d", w.Code)
	}
	if w := serve(t, h, "not-a-jwt"); w.Code != http.StatusUnauthorized {
		t.Fatalf("non-shaped bearer must 401 with no static token to fall back to, got %d", w.Code)
	}
	if w := serve(t, h, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("absent bearer must 401, got %d", w.Code)
	}
}
