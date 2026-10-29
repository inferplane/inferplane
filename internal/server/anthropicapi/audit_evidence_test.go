package anthropicapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/clientctx"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

const rawSession = "raw-session-6f1d2c"

func evidenceWriter(t *testing.T) (*audit.Writer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	return w, &buf
}

func evidenceRequest(body string, p keystore.Principal) *http.Request {
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-claude-code-request-class", "main")
	req.Header.Set("X-Claude-Code-Session-Id", rawSession)
	ctx := principal.With(req.Context(), p)
	ctx = audit.WithEvidence(ctx, time.Now(), clientctx.FromRequest(req.Header, p.KeyID, clientctx.DefaultMode()))
	return req.WithContext(clientctx.WithMode(ctx, clientctx.DefaultMode()))
}

func records(t *testing.T, buf *bytes.Buffer) []audit.Record {
	t.Helper()
	var out []audit.Record
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var r audit.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func terminal(t *testing.T, recs []audit.Record) audit.Record {
	t.Helper()
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Event == "request_completed" || (recs[i].Event == "request_started" && recs[i].Outcome != nil) {
			return recs[i]
		}
	}
	t.Fatal("no terminal record")
	return audit.Record{}
}

func fallbackRouter() *router.Router {
	provs := map[string]providers.Provider{
		"bad":  statusProvider{code: 503, body: []byte(`{"type":"error"}`)},
		"good": mockprovider.New("claude-sonnet-4-6"),
	}
	models := map[string]config.ModelConfig{"claude-sonnet-4-6": {Targets: []config.Target{
		{Provider: "bad", Model: "m1"}, {Provider: "good", Model: "claude-sonnet-4-6"},
	}}}
	return router.New(holderFor(provs, models))
}

func TestMessagesFallbackRecordsAttempts(t *testing.T) {
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerFull(fallbackRouter(), w, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, evidenceRequest(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_e", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	done := terminal(t, records(t, buf))
	if len(done.Attempts) != 2 || done.Attempts[0].Provider != "bad" || done.Attempts[0].Status != audit.AttemptUpstreamError ||
		done.Attempts[0].HTTP != 503 || done.Attempts[1].Provider != "good" || done.Attempts[1].Status != audit.AttemptOK {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
	if !done.Outcome.FallbackUsed || strings.Join(done.Outcome.FallbackChain, ",") != "bad,good" {
		t.Fatalf("outcome = %+v", done.Outcome)
	}
	if done.Latency == nil || done.Client == nil || done.Client.RequestClass != "main" || !strings.HasPrefix(done.Client.SessionRef, "s1:") {
		t.Fatalf("latency=%+v client=%+v", done.Latency, done.Client)
	}
}

func TestMessagesDenialCarriesClientAndLatency(t *testing.T) {
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerWithAudit(testRouter(), w)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, evidenceRequest(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_d", Team: "t", AllowedModels: []string{"qwen-coder"}}))
	w.Close()
	denied := terminal(t, records(t, buf))
	if denied.Event != "request_started" || denied.Outcome.Status != 403 || denied.Client == nil || denied.Latency == nil || len(denied.Attempts) != 0 {
		t.Fatalf("denied = %+v", denied)
	}
}

func TestMessagesStreamMarksFirstToken(t *testing.T) {
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerFull(testRouter(), w, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, evidenceRequest(`{"model":"claude-sonnet-4-6","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_s", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	done := terminal(t, records(t, buf))
	if done.Latency == nil || done.Latency.TTFTMs < 1 || len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptOK {
		t.Fatalf("stream record = %+v", done)
	}
}

func TestMessagesRawSessionIDNeverAudited(t *testing.T) {
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerFull(testRouter(), w, nil)
	h.ServeHTTP(httptest.NewRecorder(), evidenceRequest(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_r", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	if strings.Contains(buf.String(), rawSession) {
		t.Fatal("raw session ID reached the audit chain")
	}
}
