package responsesapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

type streamStep struct {
	event *providers.StreamEvent
	err   error
}

type streamProbe struct {
	name              string
	steps             []streamStep
	openErr           error
	onOpen            func()
	attempts, yielded int
}

func (p *streamProbe) Name() string              { return p.name }
func (*streamProbe) SupportsIngress(string) bool { return true }
func (*streamProbe) Models() []schema.ModelInfo  { return nil }
func (*streamProbe) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, errors.New("unexpected nonstream request")
}
func (p *streamProbe) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	p.attempts++
	if p.onOpen != nil {
		p.onOpen()
	}
	if p.openErr != nil {
		return nil, p.openErr
	}
	return func(yield func(*providers.StreamEvent, error) bool) {
		for _, step := range p.steps {
			p.yielded++
			if !yield(step.event, step.err) {
				return
			}
		}
	}, nil
}

func init() {
	providers.Register("responsesapi-stream-probe", func(providers.Config) (providers.Provider, error) {
		return &streamProbe{name: "mock"}, nil
	})
}

type streamAuthority struct {
	reserves, finishes int
	actual             *int64
	complete           bool
}

func (a *streamAuthority) Reserve(context.Context, governance.Subject, int64) (*governance.BudgetPermit, error) {
	a.reserves++
	return &governance.BudgetPermit{ID: "stream-permit"}, nil
}
func (a *streamAuthority) Finish(_ context.Context, _ *governance.BudgetPermit, actual *int64, complete bool) error {
	a.finishes++
	a.complete = complete
	if actual != nil {
		value := *actual
		a.actual = &value
	}
	return nil
}

type streamFixture struct {
	h               *Handler
	first, fallback *streamProbe
	authority       *streamAuthority
	writer          *audit.Writer
	audit           bytes.Buffer
	closed          bool
}

func newStreamFixture(t *testing.T) *streamFixture {
	t.Helper()
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"first":    {Type: "responsesapi-stream-probe"},
			"fallback": {Type: "responsesapi-stream-probe"},
		},
		Models: map[string]config.ModelConfig{"premium": {
			ContextWindow: 100000, Capabilities: []string{"tools"},
			Targets: []config.Target{{Provider: "first", Model: "up"}, {Provider: "fallback", Model: "up"}},
		}},
		Pricing: config.PricingConfig{OnMissing: "block", Overrides: map[string]map[string]config.RateConfig{
			"first":    {"up": {InputPerMTok: 1, OutputPerMTok: 2}},
			"fallback": {"up": {InputPerMTok: 1, OutputPerMTok: 2}},
		}},
	}
	state, _, err := live.BuildState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	holder := &live.Holder{}
	holder.Swap(state)
	f := &streamFixture{authority: &streamAuthority{}}
	first, _ := state.Provider("first")
	fallback, _ := state.Provider("fallback")
	f.first, f.fallback = first.(*streamProbe), fallback.(*streamProbe)
	f.fallback.steps = []streamStep{startStep(observedStreamUsage()), stopStep()}
	f.writer, err = audit.NewWriter("stream-test", filepath.Join(t.TempDir(), "audit.wal"), []audit.Sink{audit.NewWriterSink("test", &f.audit, true)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.close(t) })
	gov := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
	gov.SetBudgetAuthority(f.authority)
	f.h = NewHandler(router.New(holder), f.writer, gov, nil)
	return f
}

func (f *streamFixture) close(t *testing.T) {
	t.Helper()
	if !f.closed {
		f.closed = true
		if err := f.writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
func (f *streamFixture) serve(t *testing.T, w http.ResponseWriter, ctx context.Context) []audit.Record {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"premium","input":"hello","stream":true}`))
	req = req.WithContext(principal.With(ctx, keystore.Principal{Team: "team", KeyID: "opaque-key", AllowedModels: []string{"*"}, KeyOptions: keystore.KeyOptions{Owner: "user"}}))
	f.h.ServeHTTP(w, req)
	f.close(t)
	var completed []audit.Record
	scanner := bufio.NewScanner(&f.audit)
	for scanner.Scan() {
		var rec audit.Record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Event == "request_completed" {
			completed = append(completed, rec)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return completed
}
func observedStreamUsage() *schema.Usage {
	in, out := int64(10), int64(2)
	return &schema.Usage{InputTokens: &in, OutputTokens: &out}
}
func startStep(u *schema.Usage) streamStep {
	return streamStep{event: &providers.StreamEvent{
		Raw:   []byte("event: response.created\r\ndata: {\"type\":\"response.created\",\"extra\":true}\r\n\r\n"),
		Chunk: &schema.ChatChunk{Type: "message_start", Message: &schema.ChatResponse{ID: "r", Role: "assistant", Usage: u}},
	}}
}
func stopStep() streamStep {
	return streamStep{event: &providers.StreamEvent{Chunk: &schema.ChatChunk{Type: "message_stop"}}}
}
func streamErrorCode(t *testing.T, body string) string {
	t.Helper()
	var code string
	for _, frame := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
		if !strings.HasPrefix(frame, "event: error\n") {
			continue
		}
		var payload struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "event: error\ndata: ")), &payload); err != nil {
			t.Fatal(err)
		}
		if code != "" {
			t.Fatal("multiple gateway errors")
		}
		code = payload.Code
	}
	return code
}
func assertPartialFailure(t *testing.T, f *streamFixture, records []audit.Record, code string, hasUsage bool) {
	t.Helper()
	if len(records) != 1 {
		t.Fatalf("completed records=%d want 1", len(records))
	}
	rec := records[0]
	if rec.Outcome == nil || rec.Outcome.Status != 200 || !rec.Outcome.Partial || rec.Outcome.Error == nil || *rec.Outcome.Error != code {
		t.Errorf("partial failure lost: outcome=%+v want code=%q", rec.Outcome, code)
	}
	if f.first.attempts != 1 || f.fallback.attempts != 0 || f.authority.reserves != 1 || f.authority.finishes != 1 || f.authority.complete {
		t.Errorf("failure retried or lifecycle repeated: first=%d fallback=%d authority=%+v", f.first.attempts, f.fallback.attempts, f.authority)
	}
	if hasUsage {
		if rec.Usage == nil || rec.Usage.InputTokens != 10 || rec.Usage.OutputTokens != 2 || rec.Cost == nil || rec.Cost.AmountUSDMicros != 14 || f.authority.actual == nil || *f.authority.actual != 14 {
			t.Errorf("observed usage/cost lost: usage=%+v cost=%+v authority=%+v", rec.Usage, rec.Cost, f.authority)
		}
	} else if rec.Usage != nil || rec.Cost != nil || f.authority.actual != nil {
		t.Errorf("absent usage fabricated: usage=%+v cost=%+v authority=%+v", rec.Usage, rec.Cost, f.authority)
	}
}

// A fixed post-commit upstream_error or an unset audit error must fail this test.
func TestCommittedStreamPreservesTypedFailure(t *testing.T) {
	for _, hasUsage := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-usage", true: "usage"}[hasUsage], func(t *testing.T) {
			f := newStreamFixture(t)
			var u *schema.Usage
			if hasUsage {
				u = observedStreamUsage()
			}
			f.first.steps = []streamStep{startStep(u), {err: &providers.UpstreamError{StatusCode: 400, Body: []byte(`{"error":{"code":"context_length_exceeded","message":"private upstream text"}}`)}}}
			rec := httptest.NewRecorder()
			records := f.serve(t, rec, context.Background())
			if rec.Code != 200 || streamErrorCode(t, rec.Body.String()) != "context_length_exceeded" {
				t.Errorf("typed committed failure lost: status=%d body=%s", rec.Code, rec.Body.String())
			}
			assertPartialFailure(t, f, records, "context_length_exceeded", hasUsage)
		})
	}
}

// Provider messages and unknown envelope codes cannot become recovery evidence.
func TestStreamFailureDoesNotClassifyRawText(t *testing.T) {
	const secret = "ik_fake-key-do-not-expose private-prompt"
	cases := []struct {
		name  string
		cause error
	}{
		{"raw", errors.New("context_length_exceeded invalid_encrypted_content " + secret)},
		{"unknown-code", &providers.UpstreamError{StatusCode: 400, Body: []byte(`{"error":{"code":"` + secret + `","message":"context_length_exceeded"}}`)}},
		{"message-only", &providers.UpstreamError{StatusCode: 400, Body: []byte(`{"error":{"message":"invalid_encrypted_content ` + secret + `"}}`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newStreamFixture(t)
			f.first.steps = []streamStep{startStep(observedStreamUsage()), {err: tc.cause}}
			rec := httptest.NewRecorder()
			records := f.serve(t, rec, context.Background())
			assertPartialFailure(t, f, records, "upstream_error", true)
			if streamErrorCode(t, rec.Body.String()) != "upstream_error" {
				t.Fatal("classified raw text")
			}
			wire, err := json.Marshal(records)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{secret, "context_length_exceeded", "invalid_encrypted_content"} {
				if strings.Contains(rec.Body.String(), value) || bytes.Contains(wire, []byte(value)) {
					t.Errorf("private error value exposed: %q", value)
				}
			}
		})
	}
}

func TestCommittedStreamNeverRetries(t *testing.T) {
	for _, status := range []int{429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newStreamFixture(t)
			f.first.steps = []streamStep{startStep(observedStreamUsage()), {err: &providers.UpstreamError{StatusCode: status}}}
			rec := httptest.NewRecorder()
			records := f.serve(t, rec, context.Background())
			assertPartialFailure(t, f, records, "upstream_error", true)
			if rec.Code != 200 || strings.Contains(rec.Body.String(), "event: response.completed") {
				t.Fatal("committed failure became success")
			}
		})
	}
}

type disconnectedWriter struct {
	*httptest.ResponseRecorder
	writes, failAt int
}

func (w *disconnectedWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes >= w.failAt {
		return 0, errors.New("client gone: private prompt")
	}
	return w.ResponseRecorder.Write(p)
}

// Both transports must stop writing and finish once when the client disappears.
func TestClientDisconnectFinishesOnce(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint("native=", native), func(t *testing.T) {
			f := newStreamFixture(t)
			if native {
				f.first.name = "openai_responses"
			}
			f.first.steps = []streamStep{startStep(observedStreamUsage()), stopStep()}
			rec := &disconnectedWriter{ResponseRecorder: httptest.NewRecorder(), failAt: 1}
			records := f.serve(t, rec, context.Background())
			assertPartialFailure(t, f, records, "client_write_failed", true)
			if rec.writes != 1 || f.first.yielded != 1 || rec.Body.Len() != 0 {
				t.Fatalf("wrote or consumed after disconnect: writes=%d yielded=%d body=%s", rec.writes, f.first.yielded, rec.Body.String())
			}
		})
	}
	t.Run("error-frame-write", func(t *testing.T) {
		f := newStreamFixture(t)
		f.first.name = "openai_responses"
		f.first.steps = []streamStep{startStep(observedStreamUsage()), {err: context.DeadlineExceeded}}
		rec := &disconnectedWriter{ResponseRecorder: httptest.NewRecorder(), failAt: 2}
		records := f.serve(t, rec, context.Background())
		assertPartialFailure(t, f, records, "client_write_failed", true)
		if rec.writes != 2 {
			t.Fatalf("retried failed error write: %d", rec.writes)
		}
	})
}

func TestStreamDeadlineCode(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		cause      error
	}{
		{"deadline", "upstream_timeout", fmt.Errorf("transport: %w", context.DeadlineExceeded)},
		{"canceled", "request_canceled", fmt.Errorf("transport: %w", context.Canceled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStreamFixture(t)
			f.first.steps = []streamStep{startStep(observedStreamUsage()), {err: tc.cause}}
			rec := httptest.NewRecorder()
			records := f.serve(t, rec, context.Background())
			assertPartialFailure(t, f, records, tc.code, true)
			if streamErrorCode(t, rec.Body.String()) != tc.code {
				t.Fatalf("lost context code: %s", rec.Body.String())
			}
		})
	}
	t.Run("request-canceled-during-open", func(t *testing.T) {
		f := newStreamFixture(t)
		f.first.openErr = context.DeadlineExceeded
		ctx, cancel := context.WithCancel(context.Background())
		f.first.onOpen = cancel
		defer cancel()
		rec := httptest.NewRecorder()
		records := f.serve(t, rec, ctx)
		if rec.Code != 502 || f.fallback.attempts != 0 || len(records) != 1 || records[0].Outcome.Error == nil || *records[0].Outcome.Error != "upstream_timeout" {
			t.Fatalf("deadline retried or lost: status=%d records=%+v", rec.Code, records)
		}
	})
}

func TestNativeStreamBytesRemainUnchanged(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("failure=", fail), func(t *testing.T) {
			f := newStreamFixture(t)
			f.first.name = "openai_responses"
			start := startStep(observedStreamUsage())
			stop := stopStep()
			stop.event.Raw = []byte(": native comment\r\nevent: response.completed\r\ndata: { \"type\": \"response.completed\", \"extra\": true }\r\n\r\n")
			f.first.steps = []streamStep{start, stop}
			want := string(start.event.Raw) + string(stop.event.Raw)
			if fail {
				f.first.steps = []streamStep{start, {err: &providers.UpstreamError{StatusCode: 400, Body: []byte(`{"error":{"code":"invalid_encrypted_content"}}`)}}}
				want = string(start.event.Raw)
			}
			rec := httptest.NewRecorder()
			records := f.serve(t, rec, context.Background())
			if fail {
				assertPartialFailure(t, f, records, "invalid_encrypted_content", true)
				if !strings.HasPrefix(rec.Body.String(), want) || streamErrorCode(t, strings.TrimPrefix(rec.Body.String(), want)) != "invalid_encrypted_content" {
					t.Fatal("native prefix rewritten or failure lost")
				}
			} else if rec.Body.String() != want || len(records) != 1 || records[0].Outcome.Partial || records[0].Outcome.Error != nil || f.authority.finishes != 1 || !f.authority.complete {
				t.Fatalf("native bytes or completion changed: body=%q records=%+v authority=%+v", rec.Body.String(), records, f.authority)
			}
		})
	}
}

func TestIncompleteStreamKeepsObservedUsage(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, typed := range []bool{false, true} {
			t.Run(fmt.Sprintf("native=%v/typed=%v", native, typed), func(t *testing.T) {
				f := newStreamFixture(t)
				if native {
					f.first.name = "openai_responses"
				}
				f.first.steps = []streamStep{startStep(observedStreamUsage())}
				if typed {
					f.first.steps = append(f.first.steps, streamStep{err: fmt.Errorf("read: %w", responses.ErrTruncated)})
				}
				rec := httptest.NewRecorder()
				records := f.serve(t, rec, context.Background())
				assertPartialFailure(t, f, records, "stream_incomplete", true)
				if streamErrorCode(t, rec.Body.String()) != "stream_incomplete" {
					t.Fatalf("incomplete stream lost: %s", rec.Body.String())
				}
			})
		}
	}
}

func TestStreamMissingUsage(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint("native=", native), func(t *testing.T) {
			f := newStreamFixture(t)
			if native {
				f.first.name = "openai_responses"
			}
			f.first.steps = []streamStep{startStep(nil), stopStep()}
			rec := httptest.NewRecorder()
			records := f.serve(t, rec, context.Background())
			assertPartialFailure(t, f, records, "usage_missing", false)
			if streamErrorCode(t, rec.Body.String()) != "usage_missing" {
				t.Fatalf("missing usage lost: %s", rec.Body.String())
			}
		})
	}
}

func TestStreamRendererFailureRetainsLaterUsage(t *testing.T) {
	f := newStreamFixture(t)
	f.first.steps = []streamStep{startStep(nil), {event: &providers.StreamEvent{Chunk: &schema.ChatChunk{Type: "unsupported-chunk"}}}, {event: &providers.StreamEvent{Chunk: &schema.ChatChunk{Type: "message_delta", Usage: observedStreamUsage()}}}, stopStep()}
	rec := httptest.NewRecorder()
	records := f.serve(t, rec, context.Background())
	assertPartialFailure(t, f, records, "response_incompatible", true)
	if streamErrorCode(t, rec.Body.String()) != "response_incompatible" || strings.Contains(rec.Body.String(), "event: response.completed") {
		t.Fatalf("conversion failure lost: %s", rec.Body.String())
	}
}

func TestPrecommitStreamRetryStatusPreserved(t *testing.T) {
	for _, status := range []int{400, 429, 500, 503} {
		for _, duringRead := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/read=%v", status, duringRead), func(t *testing.T) {
				f := newStreamFixture(t)
				cause := &providers.UpstreamError{StatusCode: status, Body: []byte(`{"error":{"code":"context_length_exceeded"}}`)}
				if duringRead {
					f.first.steps = []streamStep{{err: cause}}
				} else {
					f.first.openErr = cause
				}
				rec := httptest.NewRecorder()
				records := f.serve(t, rec, context.Background())
				retried := status != 400
				wantStatus, wantAttempts := 400, 1
				if retried {
					wantStatus, wantAttempts = 200, 2
				}
				if rec.Code != wantStatus || len(records) != wantAttempts || f.first.attempts != 1 || f.fallback.attempts != wantAttempts-1 || f.authority.finishes != wantAttempts || records[0].Outcome.Partial || records[0].Outcome.Error == nil || *records[0].Outcome.Error != "context_length_exceeded" {
					t.Fatalf("precommit behavior changed: status=%d records=%+v authority=%+v", rec.Code, records, f.authority)
				}
			})
		}
	}
}

func TestNativeErrorFrameDoesNotInferProviderCode(t *testing.T) {
	f := newStreamFixture(t)
	f.first.name = "openai_responses"
	frame := []byte("event: error\ndata: {\"type\":\"error\",\"code\":\"context_length_exceeded\"}\n\n")
	start := startStep(observedStreamUsage())
	f.first.steps = []streamStep{start, {event: &providers.StreamEvent{Raw: frame, Chunk: &schema.ChatChunk{Type: "error"}}}}
	rec := httptest.NewRecorder()
	records := f.serve(t, rec, context.Background())
	assertPartialFailure(t, f, records, "stream_incomplete", true)
	nativePrefix := string(start.event.Raw) + string(frame)
	if !strings.HasPrefix(rec.Body.String(), nativePrefix) || streamErrorCode(t, strings.TrimPrefix(rec.Body.String(), nativePrefix)) != "stream_incomplete" {
		t.Fatal("native error rewritten or untyped provider code inferred")
	}
}

func TestPrecommitRendererFailureNeverRetries(t *testing.T) {
	f := newStreamFixture(t)
	f.first.steps = []streamStep{{event: &providers.StreamEvent{Chunk: &schema.ChatChunk{Type: "unsupported-chunk"}}}}
	rec := httptest.NewRecorder()
	records := f.serve(t, rec, context.Background())
	if rec.Code != 502 || f.first.attempts != 1 || f.fallback.attempts != 0 || len(records) != 1 || records[0].Outcome.Partial || records[0].Outcome.Error == nil || *records[0].Outcome.Error != "response_incompatible" || f.authority.finishes != 1 || f.authority.actual != nil {
		t.Fatalf("unrenderable output retried or lost: status=%d records=%+v authority=%+v", rec.Code, records, f.authority)
	}
}
