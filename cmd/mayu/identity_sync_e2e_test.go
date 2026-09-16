package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/policy"
)

func TestIdentityGatewayRefusesUntilCompatibleControlPlane(t *testing.T) {
	cfg := identity.Config{Organization: "acme", Required: true}
	id, _ := identity.NewService("acme", "sync-test")
	const key = "identity-sync-test-virtual-key"
	t.Setenv("IDENTITY_SYNC_KEY", key)
	t.Setenv("IDENTITY_SYNC_MACHINE", "identity-sync-machine")
	var compatible atomic.Bool
	var calls atomic.Int64
	rejectedReply := make(chan struct{}, 1)
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1alpha1/sync" {
			w.WriteHeader(200)
			return
		}
		var req policy.SyncRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IdentityFingerprint != cfg.Fingerprint() {
			t.Error("gateway did not send its identity declaration")
		}
		fingerprint := ""
		if compatible.Load() {
			fingerprint = cfg.Fingerprint()
		} else {
			select {
			case rejectedReply <- struct{}{}:
			default:
			}
		}
		json.NewEncoder(w).Encode(policy.SyncResponse{
			IdentityFingerprint: fingerprint, Generation: policy.GenerationOf([]v1alpha1.GovernancePolicy{}),
			IdentityPoliciesComplete: true,
			Policies:                 []v1alpha1.GovernancePolicy{}, SyncIntervalSeconds: 1,
		})
	}))
	t.Cleanup(cp.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"r","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(upstream.Close)
	dataURL, adminURL, _ := bootGateway(t, func(raw map[string]any, dir string) {
		withAnthropicProvider(upstream.URL)(raw, dir)
		raw["key_store"].(map[string]any)["identity"] = cfg
		raw["teams"] = map[string]any{"demo": map[string]any{}}
		raw["virtual_keys"] = []any{map[string]any{
			"team": "demo", "key_ref": map[string]any{"env": "IDENTITY_SYNC_KEY"},
			"allowed_models": []string{"claude-test"}, "identity": id,
		}}
		raw["control_plane"] = map[string]any{
			"url": cp.URL, "dataplane": "node",
			"token_ref": map[string]any{"env": "IDENTITY_SYNC_MACHINE"}, "require_sync": true,
		}
	})
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(path string) int {
		req, _ := http.NewRequest(http.MethodPost, dataURL+path, strings.NewReader(`{"model":"claude-test","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("X-Api-Key", key)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := call("/v1/messages"); got != 503 {
		t.Fatalf("incompatible identity sync returned %d", got)
	}
	if got := call("/v1/messages/count_tokens"); got != 200 || calls.Load() != 0 {
		t.Fatal("unready identity count reached provider or failed its 200 contract")
	}
	select {
	case <-rejectedReply:
	case <-time.After(5 * time.Second):
		t.Fatal("test did not exercise an identity-blind control-plane response")
	}
	compatible.Store(true)
	// The real failed-sync backoff is 24–30 seconds (15-second protocol floor);
	// do not change production cadence merely to make this acceptance test fast.
	deadline := time.Now().Add(40 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := client.Get(adminURL + "/readyz")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("identity synchronization did not recover within its backoff horizon")
	}
	if got := call("/v1/messages"); got != 200 || calls.Load() != 1 {
		t.Fatal("compatible identity recovery did not restore one governed request")
	}
}
