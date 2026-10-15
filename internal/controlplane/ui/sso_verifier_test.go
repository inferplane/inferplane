package ui

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/controlplane"
	"github.com/inferplane/inferplane/internal/telemetry"
)

// Drives the shipped SSO callback and request scheduler against the REAL
// resource-server verifier, discovery, JWKS fetch, signature and group mapping.
// A deliberately slow first JWKS fetch reproduces the deployed CP's in-flight
// refusal when a valid login fans out concurrent API requests. No IdP/account,
// browser install or upstream service is needed.
func TestSSOBootstrapWithColdRealVerifier(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("SSO UI verification needs Node (stdlib only)")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	var idp *httptest.Server
	var keyFetches atomic.Int64
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": idp.URL, "jwks_uri": idp.URL + "/keys",
				"authorization_endpoint": idp.URL + "/auth", "token_endpoint": idp.URL + "/token",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/keys":
			keyFetches.Add(1)
			time.Sleep(125 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
				"kty": "RSA", "kid": "fixture-key", "alg": "RS256", "use": "sig",
				"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "fixture-key"})
	claims, _ := json.Marshal(map[string]any{
		"iss": idp.URL, "aud": "fixture-client", "sub": "synthetic-operator", "token_use": "id",
		"cognito:groups": []string{"admins"}, "nonce": "fixture-nonce",
		"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	signed := b64(header) + "." + b64(claims)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	idToken := signed + "." + b64(sig)
	verifier := adminauth.NewVerifier(adminauth.VerifierConfig{Issuer: idp.URL, ClientID: "fixture-client", GroupsClaim: "cognito:groups"})
	option := controlplane.WithOIDC(verifier, adminauth.MappingConfig{AdminGroups: []string{"admins"}})
	policyPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policyPath, []byte("apiVersion: inferplane.dev/v1alpha1\nkind: GovernancePolicy\nmetadata:\n  name: fixture\nspec:\n  subject:\n    team: fixture\n  rules:\n    - name: throughput\n      failurePolicy: FailClosed\n      rate:\n        rpm: 60\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer("fixture-machine-token", policyPath, option)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	cp.Mount(mux)
	controlplane.NewUsageServer("fixture-machine-token", telemetry.NewMemoryAggregator(time.Hour), option).Mount(mux)
	gateway := httptest.NewServer(mux)
	defer gateway.Close()
	fixture, err := json.Marshal(map[string]string{"baseURL": gateway.URL, "idToken": idToken})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "testdata/sso_verifier.cjs")
	cmd.Stdin = bytes.NewReader(fixture)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SSO + real verifier regression: %v\n%s", err, out)
	}
	if got := keyFetches.Load(); got != 1 {
		t.Fatalf("expected one cold JWKS fetch, got %d", got)
	}
}
