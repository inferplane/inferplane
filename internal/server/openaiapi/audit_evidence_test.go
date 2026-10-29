package openaiapi

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

func chatWriter(t *testing.T) (*audit.Writer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	return w, &buf
}

func chatServe(h http.Handler, body string, p keystore.Principal) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("x-claude-code-request-class", "subagent")
	ctx := audit.WithEvidence(principal.With(req.Context(), p), time.Now(), clientctx.FromRequest(req.Header, p.KeyID, clientctx.DefaultMode()))
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(clientctx.WithMode(ctx, clientctx.DefaultMode())))
}

func chatTerminal(t *testing.T, buf *bytes.Buffer) audit.Record {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var r audit.Record
		if json.Unmarshal([]byte(lines[i]), &r) == nil && (r.Event == "request_completed" || (r.Event == "request_started" && r.Outcome != nil)) {
			return r
		}
	}
	t.Fatal("no terminal record")
	return audit.Record{}
}

func TestChatFallbackRecordsAttempts(t *testing.T) {
	provs := map[string]providers.Provider{"bad": statusProvider{code: 503, body: []byte(`{"error":{}}`)}, "good": mockprovider.New("claude-sonnet-4-6")}
	models := map[string]config.ModelConfig{"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "bad", Model: "m1"}, {Provider: "good", Model: "claude-sonnet-4-6"}}}}
	w, buf := chatWriter(t)
	chatServe(NewChatHandlerFull(router.New(holderFor(provs, models)), w, nil),
		`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	w.Close()
	done := chatTerminal(t, buf)
	if len(done.Attempts) != 2 || done.Attempts[0].HTTP != 503 || done.Attempts[1].Status != audit.AttemptOK || !done.Outcome.FallbackUsed || done.Latency == nil {
		t.Fatalf("chat record = %+v", done)
	}
}

func TestChatDenialCarriesClient(t *testing.T) {
	w, buf := chatWriter(t)
	chatServe(NewChatHandlerFull(testRouter(), w, nil),
		`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`, keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"other"}})
	w.Close()
	d := chatTerminal(t, buf)
	if d.Event != "request_started" || d.Client == nil || d.Client.RequestClass != "subagent" || d.Latency == nil || len(d.Attempts) != 0 {
		t.Fatalf("denial = %+v", d)
	}
}

// evChatStream is a provider whose stream yields a fixed event list, then an
// optional mid-stream error. Name() is "mock" (anthropic wire).
type evChatStream struct {
	events []*providers.StreamEvent
	err    error
}

func (evChatStream) Name() string               { return "mock" }
func (evChatStream) Models() []schema.ModelInfo { return nil }
func (evChatStream) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, errors.New("unused")
}
func (p evChatStream) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return func(yield func(*providers.StreamEvent, error) bool) {
		for _, ev := range p.events {
			if !yield(ev, nil) {
				return
			}
		}
		if p.err != nil {
			yield(nil, p.err)
		}
	}, nil
}

func chatEv(typ string) *providers.StreamEvent {
	return &providers.StreamEvent{Raw: []byte("data: {}\n\n"), Chunk: &schema.ChatChunk{Type: typ}}
}

const chatStreamBody = `{"model":"claude-sonnet-4-6","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

func chatStreamRecord(t *testing.T, p providers.Provider, req func(string, keystore.Principal) *http.Request) audit.Record {
	t.Helper()
	provs := map[string]providers.Provider{"p": p}
	models := map[string]config.ModelConfig{"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}}}}
	w, buf := chatWriter(t)
	NewChatHandlerFull(router.New(holderFor(provs, models)), w, nil).ServeHTTP(httptest.NewRecorder(),
		req(chatStreamBody, keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	return chatTerminal(t, buf)
}

func chatPlainRequest(body string, p keystore.Principal) *http.Request {
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	ctx := audit.WithEvidence(principal.With(req.Context(), p), time.Now(), clientctx.FromRequest(req.Header, p.KeyID, clientctx.DefaultMode()))
	return req.WithContext(clientctx.WithMode(ctx, clientctx.DefaultMode()))
}

func chatSlowSettleRequest(body string, p keystore.Principal) *http.Request {
	req := chatPlainRequest(body, p)
	return req.WithContext(requestpolicy.WithSettlementObserver(req.Context(), func(*audit.CostRef, *schema.Usage, bool) {
		time.Sleep(20 * time.Millisecond)
	}))
}

func TestChatStreamRecordsAttemptAndFirstToken(t *testing.T) {
	done := chatStreamRecord(t, mockprovider.New("claude-sonnet-4-6"), chatPlainRequest)
	if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptOK || done.Attempts[0].HTTP != 200 || done.Latency == nil {
		t.Fatalf("stream record = %+v", done)
	}
}

func TestChatStreamFailedAfterFirstTokenRecordsStreamFailed(t *testing.T) {
	for name, p := range map[string]providers.Provider{
		"in-band error event": evChatStream{events: []*providers.StreamEvent{chatEv("message_start"), chatEv("content_block_delta"), chatEv("error")}},
		"no terminal event":   evChatStream{events: []*providers.StreamEvent{chatEv("message_start"), chatEv("content_block_delta")}},
		"mid-stream error":    evChatStream{events: []*providers.StreamEvent{chatEv("message_start"), chatEv("content_block_delta")}, err: errors.New("reset")},
	} {
		t.Run(name, func(t *testing.T) {
			done := chatStreamRecord(t, p, chatPlainRequest)
			if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptStreamFailed || done.Attempts[0].HTTP != 200 {
				t.Fatalf("attempts = %+v", done.Attempts)
			}
		})
	}
}

func TestChatLastTargetTransportErrorRecordsNoGateway502(t *testing.T) {
	provs := map[string]providers.Provider{"p": failProvider{}}
	models := map[string]config.ModelConfig{"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}}}}
	w, buf := chatWriter(t)
	chatServe(NewChatHandlerFull(router.New(holderFor(provs, models)), w, nil),
		`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	w.Close()
	done := chatTerminal(t, buf)
	if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptUpstreamError || done.Attempts[0].HTTP != 0 {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
}

func requireChatAttemptEndedBeforeSettle(t *testing.T, done audit.Record) {
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

func TestChatStreamAttemptEndsBeforeSettlement(t *testing.T) {
	requireChatAttemptEndedBeforeSettle(t, chatStreamRecord(t, mockprovider.New("claude-sonnet-4-6"), chatSlowSettleRequest))
}

func TestChatCompleteAttemptEndsBeforeSettlement(t *testing.T) {
	provs := map[string]providers.Provider{"p": mockprovider.New("claude-sonnet-4-6")}
	models := map[string]config.ModelConfig{"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}}}}
	w, buf := chatWriter(t)
	NewChatHandlerFull(router.New(holderFor(provs, models)), w, nil).ServeHTTP(httptest.NewRecorder(),
		chatSlowSettleRequest(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	requireChatAttemptEndedBeforeSettle(t, chatTerminal(t, buf))
}
