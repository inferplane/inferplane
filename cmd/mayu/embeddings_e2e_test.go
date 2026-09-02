package main

// Embeddings lane e2e (roadmap ⑤): POST /v1/embeddings is a GOVERNED
// passthrough — RBAC gates it, the body reaches an Embedder provider
// verbatim (model rewritten to the upstream id), usage.prompt_tokens
// settles real µUSD cost, a 2xx without usage is refused (the zero-bill
// fence), and a model whose providers don't implement the optional
// interface is a clean 404.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type embedUpstream struct {
	srv      *httptest.Server
	mu       sync.Mutex
	lastBody []byte
	omitUse  bool
}

func newEmbedUpstream(t *testing.T) *embedUpstream {
	t.Helper()
	u := &embedUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.lastBody = body
		omit := u.omitUse
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if omit {
			io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"emb-upstream"}`)
			return
		}
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"emb-upstream","usage":{"prompt_tokens":7,"total_tokens":7}}`)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func postEmbeddings(t *testing.T, dataURL, key, model string) (*http.Response, string) {
	t.Helper()
	body := `{"model":"` + model + `","input":"embed this text"}`
	req, _ := http.NewRequest(http.MethodPost, dataURL+"/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/embeddings: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestE2EEmbeddingsLane(t *testing.T) {
	up := newEmbedUpstream(t)
	chatUp := newAnthropicUpstream(t)
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
		teamsAPIConfig(chatUp.srv.URL)(cfg, dir) // brings the anthropic provider "up" + admin plumbing
		cfg["providers"].(map[string]any)["emb"] = map[string]any{
			"type": "openai_compatible", "base_url": up.srv.URL,
			"api_key_ref": map[string]any{"env": "E2E_UPSTREAM_KEY"},
		}
		cfg["models"].(map[string]any)["text-embed"] = map[string]any{
			"targets": []any{map[string]any{"provider": "emb", "model": "emb-upstream"}},
		}
		cfg["pricing"].(map[string]any)["overrides"].(map[string]any)["emb"] = map[string]any{
			"emb-upstream": map[string]any{"input_per_mtok": 1000000.0}, // $1/token: 7 tokens = 7_000_000 µUSD
		}
		// A monthly budget so /v1/usage reports the settled team counter.
		cfg["teams"] = map[string]any{"emb-team": map[string]any{
			"allowed_models": []any{"text-embed"},
			"budget":         map[string]any{"usd_per_month": 100.0, "on_exceeded": "block"},
		}}
	})
	_, key := createKey(t, adminURL, "emb-team", []string{"text-embed"})

	// Served: verbatim body reaches the upstream with ONLY the model
	// rewritten to the upstream id, and the response tees verbatim.
	resp, body := postEmbeddings(t, dataURL, key, "text-embed")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("embeddings: status %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"embedding":[0.1,0.2]`) {
		t.Fatalf("upstream body not teed verbatim: %s", body)
	}
	up.mu.Lock()
	sent := string(up.lastBody)
	up.mu.Unlock()
	if !strings.Contains(sent, `"model":"emb-upstream"`) || !strings.Contains(sent, `"input":"embed this text"`) {
		t.Fatalf("upstream must see the rewritten model and the verbatim input: %s", sent)
	}

	// Settled: /v1/usage shows the µUSD the 7 prompt tokens cost.
	ureq, _ := http.NewRequest(http.MethodGet, dataURL+"/v1/usage", nil)
	ureq.Header.Set("Authorization", "Bearer "+key)
	uresp, err := http.DefaultClient.Do(ureq)
	if err != nil {
		t.Fatal(err)
	}
	ub, _ := io.ReadAll(uresp.Body)
	uresp.Body.Close()
	var usage struct {
		TeamBudget *struct {
			SpentUSDMicros int64 `json:"spent_usd_micros"`
		} `json:"team_budget"`
	}
	json.Unmarshal(ub, &usage)
	if usage.TeamBudget == nil || usage.TeamBudget.SpentUSDMicros != 7_000_000 {
		t.Fatalf("embeddings spend must settle (7 tokens @ $1/token): %s", ub)
	}

	// A model routed only to a provider WITHOUT the Embedder capability
	// (the anthropic provider) is a clean 404 on this lane.
	resp, body = postEmbeddings(t, dataURL, key, "claude-test")
	if resp.StatusCode != http.StatusForbidden { // this key allows text-embed only
		t.Fatalf("RBAC on the embeddings lane: status %d: %s", resp.StatusCode, body)
	}
	_, anyKey := createKey(t, adminURL, "emb-team", []string{"*"})
	resp, body = postEmbeddings(t, dataURL, anyKey, "claude-test")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "embeddings-capable") {
		t.Fatalf("non-Embedder provider must 404 on this lane: status %d: %s", resp.StatusCode, body)
	}

	// Zero-bill fence: a 2xx without usage is refused, never served free.
	up.mu.Lock()
	up.omitUse = true
	up.mu.Unlock()
	resp, body = postEmbeddings(t, dataURL, key, "text-embed")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "unbilled") {
		t.Fatalf("2xx without usage must be refused: status %d: %s", resp.StatusCode, body)
	}
}
