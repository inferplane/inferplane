package main

// PII egress ceiling e2e (strategy Phase 2 v1): the policy engine picks the
// action; the ingress enforces it fail-closed on the RESOLVED chain —
// blocked refuses outright, internal-only reaches only explicitly
// internal-classified providers, external-masked refuses when the masking
// filter is not active for the team.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func piiGateway(t *testing.T, upstreamURL, policyYAML string) (dataURL, adminURL string) {
	t.Helper()
	dataURL, adminURL, _ = bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(upstreamURL)(cfg, dir)
		cfg["teams"] = map[string]any{"pii-team": map[string]any{"allowed_models": []any{"*"}}}
		// A second, INTERNAL-classified provider on the same fake upstream,
		// serving claude-int. "up" (claude-test) stays unlabeled = external.
		cfg["providers"].(map[string]any)["up-int"] = map[string]any{
			"type": "anthropic", "base_url": upstreamURL,
			"api_key_ref":    map[string]any{"env": "E2E_UPSTREAM_KEY"},
			"classification": "internal",
		}
		cfg["models"].(map[string]any)["claude-int"] = map[string]any{
			"targets": []any{map[string]any{"provider": "up-int", "model": "claude-int"}},
		}
		cfg["pricing"].(map[string]any)["overrides"].(map[string]any)["up-int"] = map[string]any{
			"claude-int": map[string]any{"input_per_mtok": 1.0, "output_per_mtok": 1.0},
		}
		polDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(polDir, "pii.yaml"), []byte(policyYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})
	return dataURL, adminURL
}

func TestE2EPIIInternalOnlyCeiling(t *testing.T) {
	up := newAnthropicUpstream(t)
	pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pii-internal }
spec:
  subject: { team: pii-team }
  rules:
  - name: residency
    failurePolicy: FailClosed
    pii: { egress: internal-only }
`
	dataURL, adminURL := piiGateway(t, up.srv.URL, pol)
	_, key := createKey(t, adminURL, "pii-team", []string{"*"})

	// Internal-classified target: served.
	r := postMessages(t, dataURL, key, "claude-int")
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("internal target: status %d, want 200", r.StatusCode)
	}

	// External (unlabeled) target: refused — never egresses.
	r = postMessages(t, dataURL, key, "claude-test")
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("external target under internal-only: status %d, want 403: %s", r.StatusCode, b)
	}
	if !strings.Contains(string(b), "internal") {
		t.Fatalf("403 must name the PII restriction: %s", b)
	}
}

func TestE2EPIIBlockedAndMaskUnavailableCeilings(t *testing.T) {
	up := newAnthropicUpstream(t)
	pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pii-mixed }
spec:
  subject: { user: sub-blocked }
  rules:
  - name: wall
    failurePolicy: FailClosed
    pii: { egress: blocked }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pii-masked }
spec:
  subject: { user: sub-masked }
  rules:
  - name: mask
    failurePolicy: FailClosed
    pii: { egress: external-masked }
`
	dataURL, adminURL := piiGateway(t, up.srv.URL, pol)

	_, blockedKey := createOwnedKey(t, adminURL, "pii-team", "sub-blocked", []string{"*"})
	r := postMessages(t, dataURL, blockedKey, "claude-test")
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "blocked") {
		t.Fatalf("blocked ceiling: status %d body %s, want 403 naming the block", r.StatusCode, b)
	}

	// external-masked with NO masking filter configured for the team: the
	// mandated mask cannot run, so the request must never go external.
	_, maskedKey := createOwnedKey(t, adminURL, "pii-team", "sub-masked", []string{"*"})
	r = postMessages(t, dataURL, maskedKey, "claude-test")
	b, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "masking") {
		t.Fatalf("external-masked without an active mask: status %d body %s, want 403 naming masking", r.StatusCode, b)
	}

	// A subject with NO pii rule on the same team is untouched.
	_, freeKey := createOwnedKey(t, adminURL, "pii-team", "sub-free", []string{"*"})
	r = postMessages(t, dataURL, freeKey, "claude-test")
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("unrestricted subject: status %d, want 200", r.StatusCode)
	}
}
