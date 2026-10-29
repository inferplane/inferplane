package bedrockapi

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
)

const invokeBody = `{"anthropic_version":"bedrock-2023-05-31","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`

func invokeWriter(t *testing.T) (*audit.Writer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	return w, &buf
}

func invokeServe(h http.Handler, path string, p keystore.Principal) {
	req := httptest.NewRequest("POST", path, strings.NewReader(invokeBody))
	req.SetPathValue("modelId", strings.Split(path, "/")[2])
	req.Header.Set("x-claude-code-request-class", "main")
	ctx := audit.WithEvidence(principal.With(req.Context(), p), time.Now(), clientctx.FromRequest(req.Header, p.KeyID, clientctx.DefaultMode()))
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(clientctx.WithMode(ctx, clientctx.DefaultMode())))
}

func invokeTerminal(t *testing.T, buf *bytes.Buffer) audit.Record {
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

var anyModel = keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}}

func TestInvokeFallbackRecordsAttempts(t *testing.T) {
	provs := map[string]providers.Provider{"bad": statusProvider{code: 503, body: []byte(`{"message":"busy"}`)}, "good": &captureProvider{}}
	models := map[string]config.ModelConfig{"claude-x": {Targets: []config.Target{{Provider: "bad", Model: "m1"}, {Provider: "good", Model: "claude-x"}}}}
	w, buf := invokeWriter(t)
	h := holderFor(provs, models)
	invokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, false), "/model/claude-x/invoke", anyModel)
	w.Close()
	done := invokeTerminal(t, buf)
	if len(done.Attempts) != 2 || done.Attempts[0].HTTP != 503 || done.Attempts[1].Status != audit.AttemptOK || !done.Outcome.FallbackUsed {
		t.Fatalf("invoke record = %+v", done)
	}
}

func TestInvokeDenialCarriesClient(t *testing.T) {
	provs := map[string]providers.Provider{"p": &captureProvider{}}
	models := map[string]config.ModelConfig{"claude-x": {Targets: []config.Target{{Provider: "p", Model: "claude-x"}}}}
	w, buf := invokeWriter(t)
	h := holderFor(provs, models)
	invokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, false), "/model/claude-x/invoke",
		keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"other"}})
	w.Close()
	d := invokeTerminal(t, buf)
	if d.Event != "request_started" || d.Outcome.Status != 403 || d.Client == nil || d.Latency == nil {
		t.Fatalf("denial = %+v", d)
	}
}

func TestInvokeStreamRecordsAttempt(t *testing.T) {
	provs := map[string]providers.Provider{"p": delayedStream{}}
	models := map[string]config.ModelConfig{"claude-x": {Targets: []config.Target{{Provider: "p", Model: "claude-x"}}}}
	w, buf := invokeWriter(t)
	h := holderFor(provs, models)
	invokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, true), "/model/claude-x/invoke-with-response-stream", anyModel)
	w.Close()
	done := invokeTerminal(t, buf)
	if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptOK || done.Latency == nil {
		t.Fatalf("stream record = %+v", done)
	}
	if done.Latency.TTFTMs < 1 {
		t.Fatalf("TTFTMs = %d, want >= 1", done.Latency.TTFTMs)
	}
}

// evInvokeStream yields a fixed event list then an optional mid-stream error.
type evInvokeStream struct {
	events []*providers.StreamEvent
	err    error
}

func (evInvokeStream) Name() string               { return "mock" }
func (evInvokeStream) Models() []schema.ModelInfo { return nil }
func (evInvokeStream) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, errors.New("unused")
}
func (p evInvokeStream) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
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

func invEv(typ string) *providers.StreamEvent {
	return &providers.StreamEvent{Raw: []byte("data: {}\n\n"), Chunk: &schema.ChatChunk{Type: typ}}
}

func invokeStreamRecord(t *testing.T, p providers.Provider) audit.Record {
	t.Helper()
	provs := map[string]providers.Provider{"p": p}
	models := map[string]config.ModelConfig{"claude-x": {Targets: []config.Target{{Provider: "p", Model: "claude-x"}}}}
	w, buf := invokeWriter(t)
	h := holderFor(provs, models)
	invokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, true), "/model/claude-x/invoke-with-response-stream", anyModel)
	w.Close()
	return invokeTerminal(t, buf)
}

func TestInvokeStreamFailedAfterFirstTokenRecordsStreamFailed(t *testing.T) {
	for name, p := range map[string]providers.Provider{
		"in-band error event": evInvokeStream{events: []*providers.StreamEvent{invEv("message_start"), invEv("content_block_delta"), invEv("error")}},
		"no terminal event":   evInvokeStream{events: []*providers.StreamEvent{invEv("message_start"), invEv("content_block_delta")}},
		"mid-stream error":    evInvokeStream{events: []*providers.StreamEvent{invEv("message_start"), invEv("content_block_delta")}, err: errors.New("reset")},
	} {
		t.Run(name, func(t *testing.T) {
			done := invokeStreamRecord(t, p)
			if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptStreamFailed || done.Attempts[0].HTTP != 200 {
				t.Fatalf("attempts = %+v", done.Attempts)
			}
		})
	}
}

func TestConverseStreamRecordsAttemptOutcome(t *testing.T) {
	in, out := int64(10), int64(3)
	for name, tc := range map[string]struct {
		events func() []*providers.StreamEvent
		want   string
	}{
		"complete": {func() []*providers.StreamEvent {
			return []*providers.StreamEvent{
				converseEvent(t, "contentBlockDelta", &schema.ChatChunk{Type: "content_block_delta"}),
				converseEvent(t, "messageStop", &schema.ChatChunk{Type: "message_stop"}),
				converseEvent(t, "metadata", &schema.ChatChunk{Usage: &schema.Usage{InputTokens: &in, OutputTokens: &out}}),
			}
		}, audit.AttemptOK},
		"no metadata": {func() []*providers.StreamEvent {
			return []*providers.StreamEvent{
				converseEvent(t, "contentBlockDelta", &schema.ChatChunk{Type: "content_block_delta"}),
				converseEvent(t, "messageStop", &schema.ChatChunk{Type: "message_stop"}),
			}
		}, audit.AttemptStreamFailed},
	} {
		t.Run(name, func(t *testing.T) {
			c := &delayedNative{nativeCapture: nativeCapture{events: tc.events()}}
			h := holderFor(map[string]providers.Provider{"p": c}, map[string]config.ModelConfig{"native": {Targets: []config.Target{{Provider: "p", Model: "native-id"}}}})
			w, buf := invokeWriter(t)
			invokeServe(NewConverseHandlerMetrics(router.New(h), h, w, nil, nil, true), "/model/native/converse-stream", anyModel)
			w.Close()
			done := invokeTerminal(t, buf)
			if len(done.Attempts) != 1 || done.Attempts[0].Status != tc.want || done.Attempts[0].HTTP != 200 || done.Latency == nil {
				t.Fatalf("converse record = %+v", done)
			}
			if done.Latency.TTFTMs < 1 {
				t.Fatalf("TTFTMs = %d, want >= 1", done.Latency.TTFTMs)
			}
		})
	}
}

func TestInvokeLastTargetTransportErrorRecordsNoGateway502(t *testing.T) {
	models := map[string]config.ModelConfig{"claude-x": {Targets: []config.Target{{Provider: "p", Model: "claude-x"}}}}
	w, buf := invokeWriter(t)
	h := holderFor(map[string]providers.Provider{"p": downProvider{}}, models)
	invokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, false), "/model/claude-x/invoke", anyModel)
	w.Close()
	done := invokeTerminal(t, buf)
	if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptUpstreamError || done.Attempts[0].HTTP != 0 {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
}

type downProvider struct{ evInvokeStream }

func (downProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, errors.New("down")
}

func requireInvokeAttemptEndedBeforeSettle(t *testing.T, done audit.Record) {
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

func slowInvokeServe(h http.Handler, path string) {
	req := httptest.NewRequest("POST", path, strings.NewReader(invokeBody))
	req.SetPathValue("modelId", strings.Split(path, "/")[2])
	ctx := audit.WithEvidence(principal.With(req.Context(), anyModel), time.Now(), clientctx.FromRequest(req.Header, anyModel.KeyID, clientctx.DefaultMode()))
	ctx = requestpolicy.WithSettlementObserver(clientctx.WithMode(ctx, clientctx.DefaultMode()), func(*audit.CostRef, *schema.Usage, bool) {
		time.Sleep(20 * time.Millisecond)
	})
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))
}

func TestInvokeCompleteAttemptEndsBeforeSettlement(t *testing.T) {
	provs := map[string]providers.Provider{"p": &captureProvider{}}
	models := map[string]config.ModelConfig{"claude-x": {Targets: []config.Target{{Provider: "p", Model: "claude-x"}}}}
	w, buf := invokeWriter(t)
	h := holderFor(provs, models)
	slowInvokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, false), "/model/claude-x/invoke")
	w.Close()
	requireInvokeAttemptEndedBeforeSettle(t, invokeTerminal(t, buf))
}

func TestInvokeStreamAttemptEndsBeforeSettlement(t *testing.T) {
	provs := map[string]providers.Provider{"p": streamProvider{}}
	models := map[string]config.ModelConfig{"claude-x": {Targets: []config.Target{{Provider: "p", Model: "claude-x"}}}}
	w, buf := invokeWriter(t)
	h := holderFor(provs, models)
	slowInvokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, true), "/model/claude-x/invoke-with-response-stream")
	w.Close()
	requireInvokeAttemptEndedBeforeSettle(t, invokeTerminal(t, buf))
}

// delayedStream waits before the first event so TTFT is measurable.
type delayedStream struct{ streamProvider }

func (d delayedStream) Stream(ctx context.Context, r *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	seq, err := d.streamProvider.Stream(ctx, r)
	return func(yield func(*providers.StreamEvent, error) bool) {
		time.Sleep(5 * time.Millisecond)
		seq(yield)
	}, err
}

type delayedNative struct{ nativeCapture }

func (d *delayedNative) Stream(ctx context.Context, r *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	seq, err := d.nativeCapture.Stream(ctx, r)
	return func(yield func(*providers.StreamEvent, error) bool) {
		time.Sleep(5 * time.Millisecond)
		seq(yield)
	}, err
}

// errInvokeProvider fails both Complete and Stream with err.
type errInvokeProvider struct{ err error }

func (errInvokeProvider) Name() string               { return "mock" }
func (errInvokeProvider) Models() []schema.ModelInfo { return nil }
func (p errInvokeProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, p.err
}
func (p errInvokeProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, p.err
}

func crossModelInvokeRecord(t *testing.T, bad providers.Provider, stream bool) audit.Record {
	t.Helper()
	provs := map[string]providers.Provider{"bad": bad, "good": &captureProvider{}}
	if stream {
		provs["good"] = streamProvider{}
	}
	models := map[string]config.ModelConfig{
		"claude-opus-5": {Targets: []config.Target{{Provider: "bad", Model: "claude-opus-5"}}},
		"claude-x":      {Targets: []config.Target{{Provider: "good", Model: "claude-x"}}},
	}
	w, buf := invokeWriter(t)
	h := holderForWithFallbacks(provs, models, map[string]string{"claude-opus-5": "claude-x"})
	path := "/model/claude-opus-5/invoke"
	if stream {
		path += "-with-response-stream"
	}
	invokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, stream), path, anyModel)
	w.Close()
	return invokeTerminal(t, buf)
}

func TestInvokeCrossModelAttemptStatuses(t *testing.T) {
	for _, stream := range []bool{false, true} {
		done := crossModelInvokeRecord(t, errInvokeProvider{err: &providers.UpstreamError{StatusCode: 401, Body: []byte(`{}`)}}, stream)
		if len(done.Attempts) != 2 || done.Attempts[0].Status != audit.AttemptUpstreamError || done.Attempts[0].HTTP != 401 {
			t.Fatalf("stream=%v 401 attempts = %+v", stream, done.Attempts)
		}
		done = crossModelInvokeRecord(t, errInvokeProvider{err: &providers.UpstreamError{StatusCode: 404, Body: []byte(`{}`)}}, stream)
		if len(done.Attempts) != 2 || done.Attempts[0].Status != audit.AttemptModelNotFound || done.Attempts[0].HTTP != 404 {
			t.Fatalf("stream=%v 404 attempts = %+v", stream, done.Attempts)
		}
	}
	done := crossModelInvokeRecord(t, statusProvider{code: 404, body: []byte(`{}`)}, false)
	if len(done.Attempts) != 2 || done.Attempts[0].Status != audit.AttemptModelNotFound || done.Attempts[0].HTTP != 404 {
		t.Fatalf("status 404 attempts = %+v", done.Attempts)
	}
}
