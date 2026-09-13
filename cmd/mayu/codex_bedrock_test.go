package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

type converseObservation struct {
	path, authorization string
	body                map[string]json.RawMessage
}

func fakeConverseGateway(t *testing.T) (string, string, <-chan converseObservation) {
	t.Helper()
	observed := make(chan converseObservation, 16)
	var mu sync.Mutex
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid test request", 400)
			return
		}
		observed <- converseObservation{r.URL.Path, r.Header.Get("Authorization"), body}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			http.Error(w, "missing gateway IAM signature", 403)
			return
		}
		switch r.URL.Path {
		case "/model/test.grok/converse":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[{"text":"CONVERSE_OK"}]}},"stopReason":"end_turn","usage":{"inputTokens":20,"outputTokens":5,"totalTokens":25},"metrics":{"latencyMs":1}}`)
		case "/model/test.grok/converse-stream":
			mu.Lock()
			calls++
			current := calls
			mu.Unlock()
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			encoder := eventstream.NewEncoder()
			send := func(kind, payload string) {
				t.Helper()
				err := encoder.Encode(w, eventstream.Message{
					Headers: eventstream.Headers{
						{Name: ":message-type", Value: eventstream.StringValue("event")},
						{Name: ":event-type", Value: eventstream.StringValue(kind)},
						{Name: ":content-type", Value: eventstream.StringValue("application/json")},
					},
					Payload: []byte(payload),
				})
				if err != nil {
					t.Errorf("encode fake Converse event: %v", err)
				}
				w.(http.Flusher).Flush()
			}
			send("messageStart", `{"role":"assistant"}`)
			if current == 1 {
				send("contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"call_converse","name":"exec_command"}}}`)
				args, _ := json.Marshal(`{"cmd":"printf CODEX_CONVERSE_TOOL_OK","max_output_tokens":100}`)
				send("contentBlockDelta", fmt.Sprintf(`{"contentBlockIndex":0,"delta":{"toolUse":{"input":%s}}}`, args))
				send("contentBlockStop", `{"contentBlockIndex":0}`)
				send("messageStop", `{"stopReason":"tool_use"}`)
			} else {
				send("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"CODEX_CONVERSE_FINAL_OK"}}`)
				send("contentBlockStop", `{"contentBlockIndex":0}`)
				send("messageStop", `{"stopReason":"end_turn"}`)
			}
			send("metadata", `{"usage":{"inputTokens":20,"outputTokens":5,"totalTokens":25},"metrics":{"latencyMs":1}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	for name, value := range map[string]string{
		"AWS_ACCESS_KEY_ID": "test-access-key", "AWS_SECRET_ACCESS_KEY": "test-secret",
		"AWS_SESSION_TOKEN": "", "AWS_EC2_METADATA_DISABLED": "true",
		"AWS_CONFIG_FILE": os.DevNull, "AWS_SHARED_CREDENTIALS_FILE": os.DevNull,
		"AWS_PROFILE": "", "AWS_DEFAULT_PROFILE": "",
		"AWS_ENDPOINT_URL": upstream.URL, "AWS_ENDPOINT_URL_BEDROCK_RUNTIME": upstream.URL,
	} {
		t.Setenv(name, value)
	}
	dataURL, adminURL, _ := bootGateway(t, func(cfg map[string]any, _ string) {
		cfg["providers"] = map[string]any{"converse": map[string]any{
			"type": "bedrock", "region": "us-west-2",
		}}
		cfg["models"] = map[string]any{"grok": map[string]any{
			"context_window": 200000, "capabilities": []string{"tools"},
			"targets": []any{map[string]any{"provider": "converse", "model": "test.grok", "api": "converse"}},
		}}
		cfg["pricing"] = map[string]any{"overrides": map[string]any{
			"converse": map[string]any{"test.grok": map[string]any{"free": true}},
		}}
	})
	_, key := createKey(t, adminURL, "codex-converse", []string{"grok"})
	return dataURL, key, observed
}

func TestResponsesConverseFullGatewayEnvelope(t *testing.T) {
	dataURL, key, observed := fakeConverseGateway(t)
	req, _ := http.NewRequest(http.MethodPost, dataURL+"/v1/responses", strings.NewReader(`{"model":"grok","instructions":"system context","input":"hello","tools":[{"type":"function","name":"lookup","strict":false,"parameters":{"type":"object","properties":{}}}],"stream":false}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "CONVERSE_OK") {
		t.Fatalf("Responses-to-Converse: %d %s", resp.StatusCode, body)
	}
	got := <-observed
	if got.path != "/model/test.grok/converse" || strings.Contains(got.authorization, key) {
		t.Fatal("incorrect upstream route or client credential forwarded")
	}
	for _, field := range []string{"messages", "system", "toolConfig", "inferenceConfig"} {
		if len(got.body[field]) == 0 {
			t.Errorf("missing Converse field %s", field)
		}
	}
	for _, field := range []string{"input", "instructions", "max_output_tokens"} {
		if len(got.body[field]) != 0 {
			t.Errorf("Responses field %s leaked to Converse", field)
		}
	}
}

func TestE2ECodexCLIConverseToolRoundTrip(t *testing.T) {
	if os.Getenv("INFERPLANE_TEST_CODEX") != "1" {
		t.Skip("set INFERPLANE_TEST_CODEX=1 with codex installed")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	dataURL, key, observed := fakeConverseGateway(t)
	root := t.TempDir()
	home, workspace := filepath.Join(root, "codex-config"), filepath.Join(root, "workspace")
	for _, dir := range []string{home, workspace} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-a", "never", "exec", "--ephemeral", "--skip-git-repo-check",
		"--sandbox", "read-only", "-C", workspace, "--json",
		"-c", `model="grok"`, "-c", `model_provider="inferplane_test"`,
		"-c", fmt.Sprintf(`model_providers.inferplane_test={name="local test",base_url=%q,env_key="INFERPLANE_CONVERSE_TEST_KEY",wire_api="responses"}`, dataURL+"/v1"),
		"-c", "check_for_update_on_startup=false", "-c", `web_search="disabled"`,
		"Use the shell to print CODEX_CONVERSE_TOOL_OK, then reply CODEX_CONVERSE_FINAL_OK. Do not read or change files.")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home, "INFERPLANE_CONVERSE_TEST_KEY="+key)
	output, runErr := cmd.CombinedOutput()
	safeOutput := strings.ReplaceAll(string(output), key, "[REDACTED_GATEWAY_KEY]")
	if runErr != nil || !strings.Contains(safeOutput, "CODEX_CONVERSE_FINAL_OK") {
		t.Fatalf("Codex Converse roundtrip: %v\n%s", runErr, safeOutput)
	}
	if len(observed) < 2 {
		t.Fatal("Codex did not complete the tool loop through the gateway")
	}
	var replay bool
	for len(observed) > 0 {
		got := <-observed
		if strings.Contains(got.authorization, key) {
			t.Fatal("client credential forwarded")
		}
		replay = replay || strings.Contains(string(got.body["messages"]), "CODEX_CONVERSE_TOOL_OK")
	}
	if !replay {
		t.Fatal("tool result did not return through Converse")
	}
}
