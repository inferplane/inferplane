package main

// ADR-041 item 7 (the deferred full e2e): two mayu data planes against one
// inferplaned. Spend on ONE plane crosses the budget-tier threshold
// GLOBALLY; the control plane's judgment reaches BOTH planes' heartbeats,
// so the plane that spent NOTHING substitutes too — activation on the
// global sum through the full loop, not the control-plane unit test's view
// (`TestActiveTierFiresOnGlobalUtilizationNotPerPlane`). Plus the
// tool-calling-fidelity half: a substituted request's tools reach the
// upstream intact under the substituted model, and the upstream's tool_use
// response round-trips to the client.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/controlplane"
)

const tierE2EPolicyYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: { name: tier-pol }
spec:
  subject: { team: tier-team }
  rules:
  - name: cap
    failurePolicy: FailClosed
    budget:
      limitMilliUSD: 20000
      hardCap: true
      lease: { grantMilliUSD: 2500, renewInterval: "1s" }
  - name: downgrade-at-50
    failurePolicy: FailOpen
    routing:
      budgetTiers:
        budgetRef: cap
        tiers:
        - thresholdPercent: 50
          substitute: { claude-test: claude-cheap }
`

// toolUpstream is an Anthropic-wire upstream that records the last request
// body and always answers with a tool_use block — the fidelity probe.
type toolUpstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	last []byte
}

func newToolUpstream(t *testing.T) *toolUpstream {
	t.Helper()
	u := &toolUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.last = body
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_tool","type":"message","role":"assistant","model":"claude-cheap",`+
			`"content":[{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"Seoul"}}],`+
			`"stop_reason":"tool_use","usage":{"input_tokens":12,"output_tokens":6}}`)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *toolUpstream) lastBody() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return string(u.last)
}

func TestE2EBudgetTierActivatesFleetWideWithToolFidelity(t *testing.T) {
	t.Setenv("CP_TIER_TOKEN", "cp-tok")

	polDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(polDir, "p.yaml"), []byte(tierE2EPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := controlplane.NewServer("cp-tok", polDir)
	if err != nil {
		t.Fatal(err)
	}
	cpMux := http.NewServeMux()
	cp.Mount(cpMux)
	cpSrv := httptest.NewServer(cpMux)
	defer cpSrv.Close()

	up := newToolUpstream(t)
	bootOne := func(dpID string) (dataURL, adminURL string) {
		dataURL, adminURL, _ = bootGateway(t, func(cfg map[string]any, dir string) {
			// Economics tuned for atomic reserve/settle: each claude-test
			// request settles ~1800 milliUSD (6 output tokens at $0.3/tok)
			// with a ~2400-milli upper bound (max_tokens 8) that FITS the
			// 2500-milli lease grant — the old "one huge request overshoots
			// a tiny grant" trick is exactly what reservation now refuses.
			// Outstanding grants (2 × 2500 = 25%) stay below the 50%
			// threshold, so only settled spend can trip the tier.
			teamsAPIConfig(up.srv.URL)(cfg, dir)
			cfg["pricing"].(map[string]any)["overrides"].(map[string]any)["up"].(map[string]any)["claude-test"] = map[string]any{"input_per_mtok": 1.0, "output_per_mtok": 300000.0}
			cfg["models"].(map[string]any)["claude-cheap"] = map[string]any{
				"targets": []any{map[string]any{"provider": "up", "model": "claude-cheap"}},
			}
			cfg["pricing"].(map[string]any)["overrides"].(map[string]any)["up"].(map[string]any)["claude-cheap"] = map[string]any{"input_per_mtok": 1.0, "output_per_mtok": 1.0}
			cfg["teams"] = map[string]any{"tier-team": map[string]any{"allowed_models": []any{"*"}}}
			cfg["control_plane"] = map[string]any{
				"url":       cpSrv.URL,
				"token_ref": map[string]any{"env": "CP_TIER_TOKEN"},
				"dataplane": dpID,
			}
		})
		return dataURL, adminURL
	}
	data1, admin1 := bootOne("tier-dp1")
	data2, admin2 := bootOne("tier-dp2")
	_, key1 := createKey(t, admin1, "tier-team", []string{"*"})
	_, key2 := createKey(t, admin2, "tier-team", []string{"*"})

	postTools := func(dataURL, key string) (int, string, string) {
		body := `{"model":"claude-test","max_tokens":8,` +
			`"tools":[{"name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],` +
			`"messages":[{"role":"user","content":"weather in seoul?"}]}`
		req, _ := http.NewRequest(http.MethodPost, dataURL+"/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", key)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /v1/messages: %v", err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return r.StatusCode, string(b), r.Header.Get("x-inferplane-substituted-model")
	}

	// Drive settled spend on dp1 until the GLOBAL 50% threshold is crossed:
	// requests before the distributed policy lands settle unbudgeted (the
	// team is ungoverned until the first sync), so keep spending — three
	// COUNTED requests (~5400 of 20000 milli, plus outstanding grants)
	// cross the line. A request coming back already-substituted also ends
	// the phase: the tier is active.
	clean := 0
	deadline := time.Now().Add(10 * time.Second)
	for clean < 6 {
		st, _, sub := postTools(data1, key1)
		if sub == "claude-cheap" {
			break
		}
		if st == http.StatusOK {
			clean++
		}
		if time.Now().After(deadline) {
			t.Fatalf("dp1 spend phase stalled (last status %d, sub %q, clean %d)", st, sub, clean)
		}
		time.Sleep(400 * time.Millisecond)
	}

	// Within a heartbeat or two the control plane judges the GLOBAL
	// utilization and hands the active tier to BOTH planes — including dp2,
	// which spent (almost) nothing.
	var st2 int
	var body2, sub2 string
	deadline = time.Now().Add(6 * time.Second)
	for {
		st2, body2, sub2 = postTools(data2, key2)
		if sub2 == "claude-cheap" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dp2 (zero local spend) never substituted: status %d sub %q — tier activation did not travel the fleet", st2, sub2)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if st2 != http.StatusOK {
		t.Fatalf("substituted request on dp2: status %d: %s", st2, body2)
	}

	// Tool-calling fidelity under substitution: the upstream saw the
	// SUBSTITUTED model with the tools array intact, and the tool_use
	// response block reached the client unmodified.
	sent := up.lastBody()
	if !strings.Contains(sent, `"model":"claude-cheap"`) {
		t.Fatalf("upstream must see the substituted model: %s", sent)
	}
	if !strings.Contains(sent, `"name":"get_weather"`) || !strings.Contains(sent, `"input_schema"`) {
		t.Fatalf("tools must survive substitution verbatim: %s", sent)
	}
	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal([]byte(body2), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || len(resp.Content) != 1 || resp.Content[0].Type != "tool_use" ||
		resp.Content[0].Name != "get_weather" || !strings.Contains(string(resp.Content[0].Input), "Seoul") {
		t.Fatalf("tool_use response mangled through substitution: %s", body2)
	}

	// dp1 substitutes too (its own next request), and the latch holds: the
	// tier never de-activates within the window even though substituted
	// (cheap) traffic stops raising utilization.
	st1, _, sub1 := postTools(data1, key1)
	if st1 != http.StatusOK || sub1 != "claude-cheap" {
		t.Fatalf("dp1 post-activation: status %d sub %q, want 200 via claude-cheap", st1, sub1)
	}
}
