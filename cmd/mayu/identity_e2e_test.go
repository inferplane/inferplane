package main

// Phase 0b-1/0b-2 (durable identity, design spec
// docs/specs/2026-09-02-durable-identity-and-management-authz.md): the
// strategy §5 Identity acceptance rows, end to end — a key re-mint (or a
// second device) is ONE budget ledger when both keys carry the same
// UserID (issuer#sub), regardless of their display owner strings; and a
// policy naming the bare OIDC sub still matches a canonical-identity key.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// createIdentityKey mints a key carrying a durable user_id (full-admin
// provisioning path) plus a per-device display owner.
func createIdentityKey(t *testing.T, adminURL, team, userID, owner string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"team": team, "allowed_models": []string{"*"}, "user_id": userID, "owner": owner})
	req, _ := http.NewRequest(http.MethodPost, adminURL+"/admin/keys", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e2eAdminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("create key: decode: %v", err)
	}
	if !strings.HasPrefix(out.Plaintext, "ik_") {
		t.Fatalf("create key: no plaintext returned")
	}
	return out.Plaintext
}

func TestE2EUserBudgetSurvivesKeyRotation(t *testing.T) {
	up := newAnthropicUpstream(t)
	pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-identity }
spec:
  subject: { user: "https://idp.example#alice" }
  rules:
  - name: user-month-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 40000, hardCap: true }
`
	dataURL, adminURL := userBudgetGateway(t, up.srv.URL, pol, "id-team")

	// Two keys, two DIFFERENT owner strings (laptop/desktop), one identity.
	// Before Phase 0b these were two independent ledgers keyed on Owner.
	laptop := createIdentityKey(t, adminURL, "id-team", "https://idp.example#alice", "alice-laptop")
	desktop := createIdentityKey(t, adminURL, "id-team", "https://idp.example#alice", "alice-desktop")

	// The laptop key's request reserves within the $40 cap ($37 bound) and
	// settles $15 — the NEXT request's bound no longer fits (reserve/settle
	// economics, see govConfig in e2e_test.go).
	mustPost(t, dataURL, laptop, http.StatusOK, "first request on the laptop key")

	// The DESKTOP key's very first request must already be over the cap:
	// one person, one ledger, whatever device or key generation.
	body := mustPost(t, dataURL, desktop, http.StatusPaymentRequired, "second device shares the ledger")
	if !strings.Contains(body, "user budget exceeded") {
		t.Fatalf("402 must name the USER budget: %s", body)
	}

	// (Audit attribution — PrincipalRef.user_id on every record — is
	// asserted deterministically at the unit level in
	// internal/server/anthropicapi's TestMessagesAuditCarriesDurableIdentity;
	// the analytics index here is asynchronous.)
}

func TestE2EBareSubPolicyMatchesCanonicalIdentity(t *testing.T) {
	up := newAnthropicUpstream(t)
	// The policy names only the bare sub — the spec's explicitly weaker
	// operator convenience — and must still cap a canonical-identity key.
	pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-baresub }
spec:
  subject: { user: bob }
  rules:
  - name: user-month-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 40000, hardCap: true }
`
	dataURL, adminURL := userBudgetGateway(t, up.srv.URL, pol, "bs-team")
	key := createIdentityKey(t, adminURL, "bs-team", "https://idp.example#bob", "bob")

	mustPost(t, dataURL, key, http.StatusOK, "first request")
	body := mustPost(t, dataURL, key, http.StatusPaymentRequired, "bare-sub policy caps the canonical identity")
	if !strings.Contains(body, "user budget exceeded") {
		t.Fatalf("402 must name the USER budget: %s", body)
	}
}
