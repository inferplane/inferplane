package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/identity"
)

func TestIdentityEveryIngressAccountsAndAuditsBoundSubject(t *testing.T) {
	id, err := identity.NewService("acme", "build-bot")
	if err != nil {
		t.Fatal(err)
	}
	const key = "managed-service-key-for-local-test"
	t.Setenv("IDENTITY_GATEWAY_TEST_KEY", key)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+e2eUpstreamKey {
			t.Error("unexpected upstream route or credential")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"test","object":"chat.completion","model":"claude-test","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	t.Cleanup(upstream.Close)
	var auditPath string
	dataURL, _, shutdown := bootGateway(t, func(cfg map[string]any, dir string) {
		withAnthropicProvider(upstream.URL)(cfg, dir)
		cfg["providers"].(map[string]any)["up"].(map[string]any)["type"] = "openai_compatible"
		cfg["key_store"].(map[string]any)["identity"] = identity.Config{Organization: "acme", Required: true}
		cfg["teams"] = map[string]any{"demo": map[string]any{}}
		cfg["virtual_keys"] = []any{map[string]any{
			"team": "demo", "key_ref": map[string]any{"env": "IDENTITY_GATEWAY_TEST_KEY"},
			"allowed_models": []string{"claude-test"}, "identity": id,
		}}
		cfg["models"].(map[string]any)["claude-test"].(map[string]any)["context_window"] = 100_000
		policyPath := filepath.Join(dir, "identity-policy.json")
		body := map[string]any{
			"apiVersion": "inferplane.dev/v1alpha1", "kind": "GovernancePolicy",
			"metadata": map[string]any{"name": "person-budget"},
			"spec": map[string]any{"subject": map[string]any{"user": id.CanonicalRef()},
				"rules": []any{map[string]any{"name": "total", "failurePolicy": "FailClosed",
					"budget": map[string]any{"limitMilliUSD": 1000, "hardCap": true}}}},
		}
		raw, _ := json.Marshal(body)
		if err := os.WriteFile(policyPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		cfg["policies"] = []string{policyPath}
		auditPath = filepath.Join(dir, "audit.jsonl")
	})
	client := &http.Client{Timeout: 5 * time.Second}
	for _, tc := range []struct{ path, body string }{
		{"/v1/messages", `{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/chat/completions", `{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`},
		{"/model/claude-test/invoke", `{"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/responses", `{"model":"claude-test","input":"hello","max_output_tokens":16}`},
	} {
		req, _ := http.NewRequest(http.MethodPost, dataURL+tc.path, strings.NewReader(tc.body))
		req.Header.Set("X-Api-Key", key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s returned %d: %s", tc.path, resp.StatusCode, body)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, dataURL+"/v1/usage", nil)
	req.Header.Set("X-Api-Key", key)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var usage governance.UsageStatus
	err = json.NewDecoder(resp.Body).Decode(&usage)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || usage.UserBudget == nil || usage.UserBudget.SpentUSDMicros != 60 {
		t.Fatalf("four requests were not attributed to the same user account: %+v", usage.UserBudget)
	}
	shutdown()
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := audit.Verify(bytes.NewReader(raw))
	if err != nil || !verified.OK {
		t.Fatal("identity evidence broke the audit chain")
	}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var record audit.Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Event != "request_completed" {
			continue
		}
		if record.Principal.Identity == nil || record.Principal.Identity.Reference != id.CanonicalRef() {
			t.Fatal("completed inference lost verified identity evidence")
		}
		if bytes.Contains(scanner.Bytes(), []byte(id.Issuer)) || bytes.Contains(scanner.Bytes(), []byte(id.Subject)) {
			t.Fatal("raw issuer or subject reached inference audit")
		}
		seen[record.Request.Ingress] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, ingress := range []string{"anthropic", "openai", "bedrock", "responses"} {
		if !seen[ingress] {
			t.Fatalf("no identity-aware completion for %s", ingress)
		}
	}
}
