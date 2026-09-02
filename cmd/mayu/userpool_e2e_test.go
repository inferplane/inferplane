package main

// Two-pool user budget e2e (strategy Phase 1, design spec
// docs/specs/2026-09-02-two-pool-user-budget.md): premium pool exhausted →
// first compatible approved fallback is SERVED (never the premium model);
// no compatible fallback → 402; non-premium traffic and other users are
// untouched. Pricing: teamsAPIConfig prices claude-test at $1M/mtok, so one
// settled request (~$15) exhausts any milliUSD-scale premium pool.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// poolGateway is userBudgetGateway plus a cheap, routed fallback model
// ("claude-cheap", same fake upstream, ~µUSD per request).
func poolGateway(t *testing.T, upstreamURL, policyYAML string) (dataURL, adminURL string) {
	t.Helper()
	dataURL, adminURL, _ = bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(upstreamURL)(cfg, dir)
		cfg["teams"] = map[string]any{"pool-team": map[string]any{"allowed_models": []any{"*"}}}
		cfg["models"].(map[string]any)["claude-cheap"] = map[string]any{
			"targets": []any{map[string]any{"provider": "up", "model": "claude-cheap"}},
		}
		cfg["pricing"].(map[string]any)["overrides"].(map[string]any)["up"].(map[string]any)["claude-cheap"] = map[string]any{"input_per_mtok": 1.0, "output_per_mtok": 1.0}
		polDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(polDir, "pool.yaml"), []byte(policyYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []any{polDir}
	})
	return dataURL, adminURL
}

// postModel is postMessages for an arbitrary model, returning status, body,
// and the substitution header.
func postModel(t *testing.T, dataURL, key, model string) (int, string, string) {
	t.Helper()
	r := postMessages(t, dataURL, key, model)
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return r.StatusCode, string(b), r.Header.Get("x-inferplane-substituted-model")
}

func TestE2EUserPoolPremiumExhaustionFallsBackThenNeverServesPremium(t *testing.T) {
	up := newAnthropicUpstream(t)
	pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pool-pol }
spec:
  subject: { user: sub-pool }
  rules:
  - name: pools
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 100000
      hardCap: true
      premium:
        limitMilliUSD: 1
        models: ["claude-test"]
        fallback: ["not-routed-anywhere", "claude-cheap"]
`
	dataURL, adminURL := poolGateway(t, up.srv.URL, pol)
	_, key := createOwnedKey(t, adminURL, "pool-team", "sub-pool", []string{"*"})

	// Within the premium pool: served as requested, no substitution.
	st, _, sub := postModel(t, dataURL, key, "claude-test")
	if st != http.StatusOK || sub != "" {
		t.Fatalf("first premium request: status %d sub %q, want 200 unsubstituted", st, sub)
	}

	// Pool exhausted (~$15 spent vs a 1 milliUSD pool): the FIRST COMPATIBLE
	// fallback serves — the unrouted candidate is skipped, claude-cheap wins.
	st, _, sub = postModel(t, dataURL, key, "claude-test")
	if st != http.StatusOK || sub != "claude-cheap" {
		t.Fatalf("post-exhaustion premium request: status %d sub %q, want 200 via claude-cheap", st, sub)
	}

	// Fallback traffic does not drain the premium pool, and the premium
	// model stays substituted — never served past the pool.
	st, _, sub = postModel(t, dataURL, key, "claude-test")
	if st != http.StatusOK || sub != "claude-cheap" {
		t.Fatalf("third premium request: status %d sub %q, want 200 via claude-cheap", st, sub)
	}

	// Direct non-premium traffic is untouched — no header, no denial.
	st, _, sub = postModel(t, dataURL, key, "claude-cheap")
	if st != http.StatusOK || sub != "" {
		t.Fatalf("non-premium request: status %d sub %q, want 200 unsubstituted", st, sub)
	}
}

func TestE2EUserPoolNoCompatibleFallbackBlocks(t *testing.T) {
	up := newAnthropicUpstream(t)
	pol := `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: pool-pol-blocked }
spec:
  subject: { user: sub-walled }
  rules:
  - name: pools
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 100000
      hardCap: true
      premium:
        limitMilliUSD: 1
        models: ["claude-test"]
        fallback: ["not-routed-anywhere"]
`
	dataURL, adminURL := poolGateway(t, up.srv.URL, pol)
	_, key := createOwnedKey(t, adminURL, "pool-team", "sub-walled", []string{"*"})

	if st, _, _ := postModel(t, dataURL, key, "claude-test"); st != http.StatusOK {
		t.Fatalf("first premium request: status %d, want 200", st)
	}
	st, body, _ := postModel(t, dataURL, key, "claude-test")
	if st != http.StatusPaymentRequired {
		t.Fatalf("exhausted pool with no compatible fallback: status %d, want 402: %s", st, body)
	}
	if !strings.Contains(body, "premium") {
		t.Fatalf("402 must name the premium pool: %s", body)
	}
}
