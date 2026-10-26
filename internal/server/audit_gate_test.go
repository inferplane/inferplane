package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/server/configapi"
	"github.com/inferplane/inferplane/internal/server/routingtest"
)

func retainedAudit(t *testing.T) *audit.Writer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.wal")
	rec := audit.Record{SchemaVersion: 1, Event: "request_completed", Instance: "old", PrevHash: "sha256:genesis"}
	var backlog bytes.Buffer
	for i := 0; i < 1024; i++ {
		raw, err := rec.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		backlog.Write(raw)
		backlog.WriteByte('\n')
		sum := sha256.Sum256(raw)
		rec.PrevHash = "sha256:" + hex.EncodeToString(sum[:])
	}
	if err := os.WriteFile(path, backlog.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := audit.NewWriter("new", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestDataMuxAuditPressureGateUsesIngressErrorShapeAfterAuth(t *testing.T) {
	for _, path := range []string{
		"/v1/messages", "/v1/chat/completions", "/v1/responses",
		"/model/premium/invoke", "/model/premium/invoke-with-response-stream",
		"/model/premium/converse", "/model/premium/converse-stream",
	} {
		t.Run(path, func(t *testing.T) {
			f := routingtest.New(t, nil)
			body := `{"model":"premium","max_tokens":16,"input":"hello","messages":[{"role":"user","content":"hello"}]}`
			if strings.HasSuffix(path, "/converse") || strings.HasSuffix(path, "/converse-stream") {
				f = localConverseFixture(t)
				body = localConverseBody
			}
			h := policyMux(f, retainedAudit(t), nil, nil, nil)
			if r := policyDo(h, path, body, false); r.Code != http.StatusUnauthorized {
				t.Fatalf("audit readiness leaked before authentication: %d", r.Code)
			}
			r := policyDo(h, path, body, true)
			if r.Code != http.StatusServiceUnavailable || r.Header().Get("Retry-After") == "" {
				t.Fatalf("retained backlog admitted new inference: %d %s", r.Code, r.Body.String())
			}
			if len(f.Public.Calls)+len(f.Private.Calls) != 0 {
				t.Fatal("audit refusal dispatched a provider request")
			}
			var env map[string]any
			if err := json.Unmarshal(r.Body.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.HasPrefix(path, "/model/"):
				if env["message"] == nil || r.Header().Get("X-Amzn-ErrorType") != "ServiceUnavailableException" {
					t.Fatalf("wrong Bedrock error: %v %v", env, r.Header())
				}
			case path == "/v1/messages":
				if env["type"] != "error" || env["error"].(map[string]any)["type"] != "api_error" {
					t.Fatalf("wrong Anthropic error: %v", env)
				}
			default:
				errBody := env["error"].(map[string]any)
				if errBody["type"] != "server_error" || errBody["code"] != "audit_unavailable" {
					t.Fatalf("wrong OpenAI error: %v", env)
				}
			}
		})
	}
}

func TestNativeConverseEvaluationRemainsBehindAuditPressureGate(t *testing.T) {
	f := localConverseFixture(t)
	w := retainedAudit(t)
	h := WithEvaluations(http.NotFoundHandler(), policyMux(f, w, nil, nil, nil),
		[]string{"admin-fixture"}, nil, adminauth.MappingConfig{}, nil)
	req := httptest.NewRequest("POST", "/admin/evaluations", strings.NewReader(
		`{"protocol":"bedrock-converse","model":"premium","body":`+localConverseBody+`}`))
	req.Header.Set("Authorization", "Bearer admin-fixture")
	req.Header.Set("X-Inferplane-Virtual-Key", "dev-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var result evaluationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || result.HTTPStatus != 503 || result.Outcome != "http_error" ||
		result.Attempts != 0 || result.Settled || result.AccountingKnown || len(f.Public.Calls) != 0 {
		t.Fatalf("evaluation bypassed native audit gate: %d %+v calls=%d", rec.Code, result, len(f.Public.Calls))
	}
}

func TestDataMuxAuditPressurePreservesLocalCountsAndModels(t *testing.T) {
	f := routingtest.New(t, nil)
	h := policyMux(f, retainedAudit(t), nil, nil, nil)
	for _, path := range []string{"/v1/messages/count_tokens", "/model/premium/count-tokens"} {
		body := `{"model":"premium","messages":[{"role":"user","content":"hello"}]}`
		if strings.HasPrefix(path, "/model/") {
			body = countBody("bedrock", body)
		}
		if r := policyDo(h, path, body, true); r.Code != http.StatusOK {
			t.Fatalf("audit failure breaks count contract: %d %s", r.Code, r.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("x-api-key", "dev-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit failure breaks model catalog: %d", rec.Code)
	}
	if len(f.Public.Calls)+len(f.Private.Calls) != 0 {
		t.Fatal("unready audit count routes called a provider instead of counting locally")
	}
}

func TestAdminMuxReadyzIncludesAuditPressureButHealthRemainsLive(t *testing.T) {
	w := retainedAudit(t)
	h := AdminMux(stubStore{}, nil, nil, adminauth.MappingConfig{}, func() configapi.View { return configapi.View{} },
		nil, w, nil, func() (bool, string) { return true, "" }, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, tc := range []struct {
		path string
		want int
	}{{"/readyz", 503}, {"/healthz", 200}} {
		t.Run(fmt.Sprintf("%s/%d", tc.path, tc.want), func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
			if rec.Code != tc.want {
				t.Fatalf("%s = %d %s", tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}
