package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/identity"
)

func TestIdentityAuditLegacyBytesAndMixedChain(t *testing.T) {
	const old = `{"schema_version":1,"event":"request_started","id":"old","ts":"2026-09-16T00:00:00Z","instance":"i","principal":{"key_id":"key","team":"t","user":"legacy-owner","auth_method":"oidc"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"trace_id":null,"prev_hash":"sha256:genesis"}`
	var rec Record
	if err := json.Unmarshal([]byte(old), &rec); err != nil {
		t.Fatal(err)
	}
	raw, _ := rec.Canonical()
	if string(raw) != old {
		t.Fatal("identity addition changed literal legacy bytes")
	}
	id, err := identity.NewHuman("org", "https://issuer.example", "private-subject")
	if err != nil {
		t.Fatal(err)
	}
	evidence := id.Audit()
	hash := func(b []byte) string { sum := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(sum[:]) }
	rec.ID, rec.PrevHash = "new", hash([]byte(old))
	rec.Principal.Identity, rec.Principal.User = &evidence, nil
	next, _ := rec.Canonical()
	if strings.Contains(string(next), id.Issuer) || strings.Contains(string(next), id.Subject) || !strings.Contains(string(next), id.CanonicalRef()) {
		t.Fatal("new identity audit exposes raw identity or omits canonical reference")
	}
	rec.ID, rec.PrevHash, rec.Principal.Identity = "old-again", hash(next), nil
	last, _ := rec.Canonical()
	fixture := old + "\n" + string(next) + "\n" + string(last) + "\n"
	result, err := Verify(strings.NewReader(fixture))
	if err != nil || !result.OK || result.Records != 3 {
		t.Fatalf("mixed identity chain failed: %+v %v", result, err)
	}
	broken := strings.Replace(fixture, id.CanonicalRef(), "identity-v1:tampered", 1)
	result, err = Verify(strings.NewReader(broken))
	if err != nil || result.OK || result.BrokenAt != 3 {
		t.Fatal("identity tampering was not detected")
	}
}
