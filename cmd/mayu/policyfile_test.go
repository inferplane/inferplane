package main

// E2E tests for the local GovernancePolicy file channel (ADR-033): documents
// from the config "policies" key are enforced live, and a policy file
// contributes ONLY the dimensions its rules declare — the review-finding
// regression where a modelAccess-only policy manufactured an "unlimited"
// team entry that shadowed the config budget.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

const policyFileYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-team-models }
spec:
  subject: { team: pol-team }
  rules:
  - name: test-model-only
    failurePolicy: FailOpen
    modelAccess: { allow: ["claude-test"] }
`

// A modelAccess-only policy must (a) enforce the model allow-list and
// (b) leave the team's CONFIG budget fully in force — file dimensions
// overlay, they never replace the whole team policy.
func TestE2EPolicyFileModelAccessOverlaysConfigBudget(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		// Second configured model so the policy has something to deny.
		cfg["models"].(map[string]any)["claude-other"] = map[string]any{
			"targets": []any{map[string]any{"provider": "up", "model": "claude-other"}},
		}
		cfg["pricing"].(map[string]any)["overrides"].(map[string]any)["up"].(map[string]any)["claude-other"] = map[string]any{"input_per_mtok": 1000000.0, "output_per_mtok": 1000000.0}
		// Config team with a budget so tiny one settled request exhausts it.
		cfg["teams"] = map[string]any{
			"pol-team": map[string]any{
				"allowed_models": []any{"*"},
				"budget":         map[string]any{"usd_per_month": 0.000001, "on_exceeded": "block"},
			},
		}
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(polDir, "team.yaml"), []byte(policyFileYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})

	_, key := createKey(t, adminURL, "pol-team", []string{"*"})

	// (a) modelAccess enforces: the policy-denied model 403s even though the
	// key's own allow-list is a wildcard.
	r := postMessages(t, dataURL, key, "claude-other")
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("policy-denied model: status %d, want 403", r.StatusCode)
	}

	// (b) the config budget survives the overlay: first allowed request
	// settles past the tiny limit, second blocks with 402. Before the fix,
	// the modelAccess-only policy manufactured an unlimited team entry and
	// this second request sailed through.
	r1 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first allowed request: status %d, want 200", r1.StatusCode)
	}
	r2 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("request after budget exhausted: status %d, want 402 (config budget must survive a modelAccess-only policy)", r2.StatusCode)
	}
}

// A policy budget rule DOES replace the budget dimension (file wins where it
// speaks) while leaving other dimensions to the base.
func TestE2EPolicyFileBudgetOverridesConfig(t *testing.T) {
	up := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(up.srv.URL)(cfg, dir)
		// Config says a roomy budget; the policy file caps it at 1 milliUSD.
		cfg["teams"] = map[string]any{
			"pol-team": map[string]any{
				"allowed_models": []any{"*"},
				"budget":         map[string]any{"usd_per_month": 1000.0, "on_exceeded": "block"},
			},
		}
		polDir := filepath.Join(dir, "policies")
		if err := os.MkdirAll(polDir, 0o700); err != nil {
			t.Fatal(err)
		}
		pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pol-team-cap }
spec:
  subject: { team: pol-team }
  rules:
  - name: hard-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 1, hardCap: true }
`
		if err := os.WriteFile(filepath.Join(polDir, "cap.yaml"), []byte(pol), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})

	_, key := createKey(t, adminURL, "pol-team", []string{"*"})

	// First request settles past the 1 milliUSD file cap (pre-check sees zero
	// spend, §5.3), second blocks — the file budget bound, not the config's $1000.
	r1 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first request: status %d, want 200", r1.StatusCode)
	}
	r2 := postMessages(t, dataURL, key, "claude-test")
	io.Copy(io.Discard, r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("second request: status %d, want 402 (file budget must bind)", r2.StatusCode)
	}
}
