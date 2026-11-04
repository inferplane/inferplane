package anthropicapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
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
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
	"github.com/inferplane/inferplane/pkg/schema"
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

// fallbackRouter chains two providers for ONE model (m1 is only the upstream
// id), so the step between them is same-model, not a cross-model boundary;
// the 404 cross-model case below uses model_fallbacks instead.
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

// evStream is a configurable provider for stream/complete evidence tests.
type evStream struct {
	events    []*providers.StreamEvent
	midErr    error
	streamErr error
	complete  error
}

func (evStream) Name() string               { return "ev" }
func (evStream) Models() []schema.ModelInfo { return nil }
func (p evStream) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, p.complete
}
func (p evStream) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	return func(yield func(*providers.StreamEvent, error) bool) {
		for _, e := range p.events {
			if !yield(e, nil) {
				return
			}
		}
		if p.midErr != nil {
			yield(nil, p.midErr)
		}
	}, nil
}

func chunkEv(typ string) *providers.StreamEvent {
	return &providers.StreamEvent{Raw: []byte("event: " + typ + "\ndata: {}\n\n"), Chunk: &schema.ChatChunk{Type: typ}}
}

func serveEvidence(t *testing.T, prov providers.Provider, stream bool) audit.Record {
	t.Helper()
	w, buf := evidenceWriter(t)
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}}
	h := NewMessagesHandlerFull(router.New(holderFor(map[string]providers.Provider{"p": prov}, models)), w, nil)
	body := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	if stream {
		body = `{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	}
	h.ServeHTTP(httptest.NewRecorder(), evidenceRequest(body, keystore.Principal{KeyID: "ik_x", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	return terminal(t, records(t, buf))
}

func TestMessagesStreamInBandErrorIsStreamFailed(t *testing.T) {
	done := serveEvidence(t, evStream{events: []*providers.StreamEvent{chunkEv("message_start"), chunkEv("error")}}, true)
	if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptStreamFailed {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
}

func TestMessagesStreamEOFWithoutStopIsStreamFailed(t *testing.T) {
	done := serveEvidence(t, evStream{events: []*providers.StreamEvent{chunkEv("message_start")}}, true)
	if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptStreamFailed {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
}

func TestMessagesStreamMidErrorIsStreamFailedPartial(t *testing.T) {
	done := serveEvidence(t, evStream{events: []*providers.StreamEvent{chunkEv("message_start")}, midErr: errors.New("boom")}, true)
	if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptStreamFailed || !done.Outcome.Partial {
		t.Fatalf("attempts = %+v outcome = %+v", done.Attempts, done.Outcome)
	}
}

// slowSettle delays settlement by 20ms through the request-scoped settlement
// observer, a seam that runs after the provider exchange and before the audit
// record is written. The final attempt must have ended before that delay.
func slowSettleRequest(body string, p keystore.Principal) *http.Request {
	req := evidenceRequest(body, p)
	return req.WithContext(requestpolicy.WithSettlementObserver(req.Context(), func(*audit.CostRef, *schema.Usage, bool) {
		time.Sleep(20 * time.Millisecond)
	}))
}

func requireAttemptEndedBeforeSettle(t *testing.T, done audit.Record) {
	t.Helper()
	if len(done.Attempts) == 0 || done.Latency == nil {
		t.Fatalf("record = %+v", done)
	}
	a := done.Attempts[len(done.Attempts)-1]
	if a.Status != audit.AttemptOK || a.HTTP != 200 {
		t.Fatalf("attempt = %+v", a)
	}
	if gap := done.Latency.TotalMs - (a.StartMs + a.DurMs); gap < 15 {
		t.Fatalf("attempt %+v includes settlement: total %d, gap %d ms (want >= 15)", a, done.Latency.TotalMs, gap)
	}
}

func TestMessagesStreamAttemptEndsBeforeSettlement(t *testing.T) {
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerFull(testRouter(), w, nil)
	h.ServeHTTP(httptest.NewRecorder(), slowSettleRequest(`{"model":"claude-sonnet-4-6","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_t1", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	requireAttemptEndedBeforeSettle(t, terminal(t, records(t, buf)))
}

func TestMessagesCompleteAttemptEndsBeforeSettlement(t *testing.T) {
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerFull(testRouter(), w, nil)
	h.ServeHTTP(httptest.NewRecorder(), slowSettleRequest(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_t2", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	requireAttemptEndedBeforeSettle(t, terminal(t, records(t, buf)))
}

func TestMessagesStreamPreTTFT404CrossModelIsModelNotFound(t *testing.T) {
	provs := map[string]providers.Provider{
		"bad":  evStream{streamErr: &providers.UpstreamError{StatusCode: 404, Body: []byte(`{}`)}},
		"good": mockprovider.New("claude-sonnet-4-6"),
	}
	models := map[string]config.ModelConfig{
		"claude-opus-5":     {Targets: []config.Target{{Provider: "bad", Model: "claude-opus-5"}}},
		"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "good", Model: "claude-sonnet-4-6"}}},
	}
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerFull(router.New(holderForWithFallbacks(provs, models, map[string]string{"claude-opus-5": "claude-sonnet-4-6"})), w, nil)
	h.ServeHTTP(httptest.NewRecorder(), evidenceRequest(`{"model":"claude-opus-5","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_n", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	done := terminal(t, records(t, buf))
	if len(done.Attempts) != 2 || done.Attempts[0].Status != audit.AttemptModelNotFound || done.Attempts[0].HTTP != 404 {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
}

func TestMessagesLastTargetTransportErrorRecordsNoGateway502(t *testing.T) {
	for _, stream := range []bool{false, true} {
		done := serveEvidence(t, evStream{complete: errors.New("down"), streamErr: errors.New("down")}, stream)
		if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptUpstreamError || done.Attempts[0].HTTP != 0 {
			t.Fatalf("stream=%v attempts = %+v", stream, done.Attempts)
		}
	}
}

func TestMessagesLastTargetDeadlineIsTimeout(t *testing.T) {
	done := serveEvidence(t, evStream{complete: context.DeadlineExceeded}, false)
	if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptTimeout {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
}
