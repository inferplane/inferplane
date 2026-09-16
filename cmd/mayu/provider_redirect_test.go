package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestE2ECountTokensRedirectReturnsLocalEstimate(t *testing.T) {
	for _, authMode := range []string{"api-key", "bearer"} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("%s/%d", authMode, status), func(t *testing.T) {
				const sensitive = "synthetic-sensitive-redirect-content"
				var sourceCalls, destinationCalls atomic.Int64
				destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					destinationCalls.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					http.Error(w, sensitive, http.StatusBadGateway)
				}))
				t.Cleanup(destination.Close)
				location := destination.URL + "/" + sensitive
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sourceCalls.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/v1/messages/count_tokens" {
						t.Error("source received a request outside the count endpoint")
					}
					header, want := "X-Api-Key", e2eUpstreamKey
					if authMode == "bearer" {
						header, want = "Authorization", "Bearer "+e2eUpstreamKey
					}
					if r.Header.Get(header) != want {
						t.Error("count source did not receive the configured synthetic gateway credential")
					}
					w.Header().Set("Location", location)
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(status)
					fmt.Fprint(w, sensitive)
				}))
				t.Cleanup(source.Close)
				dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, dir string) {
					withAnthropicProvider(source.URL)(cfg, dir)
					if authMode == "bearer" {
						cfg["providers"].(map[string]any)["up"].(map[string]any)["auth_header"] = "bearer"
					}
				})
				_, key := createKey(t, adminURL, "demo", []string{"claude-test"})
				body := `{"model":"claude-test","messages":[{"role":"user","content":"hello world from the redirect regression"}]}`
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, dataURL+"/v1/messages/count_tokens", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("X-Api-Key", key)
				req.Header.Set("Content-Type", "application/json")
				// Keep default redirect following: a leaked gateway redirect must
				// be observable as a destination call, not hidden by a test client.
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal("count request failed")
				}
				defer resp.Body.Close()
				got, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal("count response could not be read")
				}
				if sourceCalls.Load() != 1 || destinationCalls.Load() != 0 {
					t.Fatalf("count source_calls=%d destination_calls=%d, want 1/0", sourceCalls.Load(), destinationCalls.Load())
				}
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("count status=%d, want 200", resp.StatusCode)
				}
				if resp.Request.URL.String() != req.URL.String() || resp.Header.Get("Location") != "" {
					t.Fatal("count response relayed an upstream redirect")
				}
				for _, secret := range []string{sensitive, location, e2eUpstreamKey, key} {
					if bytes.Contains(got, []byte(secret)) || strings.Contains(fmt.Sprint(resp.Header), secret) {
						t.Fatal("count response exposed sensitive upstream data")
					}
				}
				var count struct {
					InputTokens int64 `json:"input_tokens"`
				}
				if err := json.Unmarshal(got, &count); err != nil || count.InputTokens <= 0 {
					t.Fatal("count response is not valid JSON with a positive input_tokens estimate")
				}
			})
		}
	}
}
