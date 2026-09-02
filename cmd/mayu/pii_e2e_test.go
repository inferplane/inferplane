package main

// PII egress ceiling e2e (strategy Phase 2 v1): the policy engine picks the
// action; the ingress enforces it fail-closed on the RESOLVED chain —
// blocked refuses outright, internal-only reaches only explicitly
// internal-classified providers, external-masked refuses when the masking
// filter is not active for the team, and external-unmodified egresses
// verbatim only after a completed detector pass reports nothing protected
// (no detector, a detector error, or a hit all refuse).

import (
	"bytes"
	"encoding/json"
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

// postMessagesContent is postMessages with a caller-chosen user message, for
// driving the PII detector.
func postMessagesContent(t *testing.T, dataURL, key, model, content string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 16,
		"messages": []any{map[string]any{"role": "user", "content": content}},
	})
	req, _ := http.NewRequest(http.MethodPost, dataURL+"/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return r.StatusCode, string(b)
}

const piiUnmodifiedPolicy = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pii-verified }
spec:
  subject: { user: sub-verified }
  rules:
  - name: verify
    failurePolicy: FailClosed
    pii: { egress: external-unmodified }
`

func TestE2EPIIExternalUnmodifiedCeiling(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		cfg["teams"] = map[string]any{"pii-team": map[string]any{"allowed_models": []any{"*"}}}
		cfg["plugins"] = []any{map[string]any{"name": "pii-mask", "teams": []any{"pii-team"}}}
		polDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(polDir, "pii.yaml"), []byte(piiUnmodifiedPolicy), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})
	_, key := createOwnedKey(t, adminURL, "pii-team", "sub-verified", []string{"*"})

	// Detector-verified clean: served, and the upstream saw the text verbatim.
	st, body := postMessagesContent(t, dataURL, key, "claude-test", "summarize the quarterly report")
	if st != http.StatusOK {
		t.Fatalf("clean request under external-unmodified: status %d, want 200: %s", st, body)
	}

	// Detector hit (an email address): must never leave, modified or not.
	st, body = postMessagesContent(t, dataURL, key, "claude-test", "email alice@example.com the report")
	if st != http.StatusForbidden || !strings.Contains(body, "protected") {
		t.Fatalf("PII under external-unmodified: status %d body %s, want 403 naming protected content", st, body)
	}
}

func TestE2EPIIExternalUnmodifiedWithoutDetectorRefuses(t *testing.T) {
	up := newAnthropicUpstream(t)
	// No plugins block at all: the "nothing protected" claim cannot be
	// verified, so every request under the ceiling refuses.
	dataURL, adminURL := piiGateway(t, up.srv.URL, piiUnmodifiedPolicy)
	_, key := createOwnedKey(t, adminURL, "pii-team", "sub-verified", []string{"*"})

	st, body := postMessagesContent(t, dataURL, key, "claude-test", "summarize the quarterly report")
	if st != http.StatusForbidden || !strings.Contains(body, "detector") {
		t.Fatalf("external-unmodified without a detector: status %d body %s, want 403 naming the detector", st, body)
	}
}
