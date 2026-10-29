package bedrockapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/clientctx"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

// Payloads follow the Claude Code 2.1.312 capture of a Bedrock
// invoke-with-response-stream main turn (spec "Early verification results",
// mode (a)); the tool-use id and message id are placeholders. Only
// not_flagged and empty tool_uses were observed; flagged is inferred.

const (
	amToolUse   = "toolu_bdrk_01Fixture00000000000000"
	amSession   = "raw-session-bedrock-6f1d2c"
	amUnparsed  = "inferplane_audit_auto_mode_unparsed_total"
	amMainBody  = `{"anthropic_version":"bedrock-2023-05-31","max_tokens":128000,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"run ls"}]}`
	amBlockBody = `{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,"stop_sequences":["</block>"],"messages":[{"role":"user","content":"classify"}]}`
)

// bedrockInvokeStream replays InvokeModelWithResponseStream chunk payloads
// exactly as providers/bedrock streamInvoke canonicalizes them
// (providers/bedrock/invoke.go:128-136): json.Unmarshal into
// schema.ChatChunk, whose Delta keeps the upstream delta object verbatim,
// and Raw re-rendered as Anthropic SSE. Name "mock" passes
// servesBedrockIngress.
type bedrockInvokeStream struct{ payloads []string }

func (bedrockInvokeStream) Name() string               { return "mock" }
func (bedrockInvokeStream) Models() []schema.ModelInfo { return nil }
func (bedrockInvokeStream) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	return nil, errors.New("unused")
}
func (p bedrockInvokeStream) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return func(yield func(*providers.StreamEvent, error) bool) {
		for _, payload := range p.payloads {
			ev := &providers.StreamEvent{}
			var c schema.ChatChunk
			if json.Unmarshal([]byte(payload), &c) == nil {
				ev.Chunk = &c
				var b strings.Builder
				if schema.WriteAnthropicSSE(&b, &c) == nil {
					ev.Raw = []byte(b.String())
				}
			}
			if !yield(ev, nil) {
				return
			}
		}
	}, nil
}

// bedrockInvokeReply answers Complete with a fixed Anthropic-format body, as
// providers/bedrock completeInvoke does (Parsed from the same bytes).
type bedrockInvokeReply struct{ body string }

func (bedrockInvokeReply) Name() string               { return "mock" }
func (bedrockInvokeReply) Models() []schema.ModelInfo { return nil }
func (p bedrockInvokeReply) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	var r schema.ChatResponse
	if err := json.Unmarshal([]byte(p.body), &r); err != nil {
		return nil, err
	}
	return &providers.ProxyResponse{StatusCode: 200, RawBody: []byte(p.body), Parsed: &r}, nil
}
func (bedrockInvokeReply) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, errors.New("unused")
}

func amMainPayloads(safeguardResults string, stop bool) []string {
	out := []string{
		`{"type":"message_start","message":{"model":"claude-opus-5-5","id":"msg_bdrk_fixture","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":2,"cache_creation_input_tokens":29971,"cache_read_input_tokens":18427,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I will run it. <block>yes</block>"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"` + amToolUse + `","name":"Bash","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null,"stop_details":null,"container":null,"safeguard_results":` + safeguardResults +
			`},"usage":{"cache_creation_input_tokens":29971,"cache_read_input_tokens":18427,"input_tokens":2,"output_tokens":287,"output_tokens_details":{"thinking_tokens":151}}}`,
	}
	if stop {
		out = append(out, `{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":2,"outputTokenCount":287}}`)
	}
	return out
}

func amSafeguard(status string) string {
	return `[{"type":"dangerous_tool_use","status":` + status + `}]`
}

func amToolOutcome(outcome string) string {
	return amSafeguard(`{"type":"available","tool_uses":{"` + amToolUse + `":{"type":"evaluated","outcome":"` + outcome + `"}}}`)
}

type amInvokeRun struct {
	body     []byte
	done     audit.Record
	line     string
	all      string
	unparsed float64
}

type amHandler func(r *router.Router, h *live.Holder, w *audit.Writer, m *metrics.Metrics) http.Handler

func invokeStreamHandler(r *router.Router, h *live.Holder, w *audit.Writer, m *metrics.Metrics) http.Handler {
	return NewInvokeHandlerMetrics(r, h, w, nil, m, true)
}

func invokeCompleteHandler(r *router.Router, h *live.Holder, w *audit.Writer, m *metrics.Metrics) http.Handler {
	return NewInvokeHandlerMetrics(r, h, w, nil, m, false)
}

// serveInvokeAutoMode runs one request through the real evidence middleware;
// Claude Code 2.1.312 sends the session header but no request class.
func serveInvokeAutoMode(t *testing.T, mk amHandler, prov providers.Provider, model, path, body string, mode clientctx.Mode) amInvokeRun {
	t.Helper()
	w, buf := invokeWriter(t)
	m := metrics.New()
	holder := holderFor(map[string]providers.Provider{"p": prov}, map[string]config.ModelConfig{model: {Targets: []config.Target{{Provider: "p", Model: model + "-id"}}}})
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.SetPathValue("modelId", strings.Split(path, "/")[2])
	req.Header.Set("X-Claude-Code-Session-Id", amSession)
	p := keystore.Principal{KeyID: "ik_am", Team: "t", AllowedModels: []string{"*"}}
	rec := httptest.NewRecorder()
	clientctx.Middleware(mode, mk(router.New(holder), holder, w, m)).ServeHTTP(rec, req.WithContext(principal.With(req.Context(), p)))
	w.Close()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	out := amInvokeRun{body: rec.Body.Bytes(), all: buf.String(), unparsed: gatheredCounter(t, m, amUnparsed)}
	out.done = invokeTerminal(t, buf)
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(l, `"id":"`+out.done.ID+`"`) {
			out.line = l
		}
	}
	return out
}

func gatheredCounter(t *testing.T, m *metrics.Metrics, name string) float64 {
	t.Helper()
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("%s not gathered", name)
	return 0
}

func requireNoResponseText(t *testing.T, auditText string) {
	t.Helper()
	for _, s := range []string{"<block>", "block>", amToolUse, amSession, "safeguard", "not_flagged"} {
		esc, _ := json.Marshal(s)
		if strings.Contains(auditText, s) || strings.Contains(auditText, strings.Trim(string(esc), `"`)) {
			t.Fatalf("%q reached the audit chain:\n%s", s, auditText)
		}
	}
}

func requireAutoMode(t *testing.T, got *audit.AutoModeRef, verdict, stage string) {
	t.Helper()
	if got == nil || got.Verdict != verdict || got.Stage != stage || got.Signature != clientctx.AutoModeSignature {
		t.Fatalf("auto_mode = %+v, want %s/%s/%s", got, verdict, stage, clientctx.AutoModeSignature)
	}
}

// comparable strips the fields that legitimately differ between two runs of
// the same request (ids, times, chain links, durations) and auto_mode.
func comparable(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "ts", "prev_hash", "latency", "auto_mode"} {
		delete(m, k)
	}
	if attempts, ok := m["attempts"].([]any); ok {
		for _, a := range attempts {
			delete(a.(map[string]any), "start_ms")
			delete(a.(map[string]any), "dur_ms")
		}
	}
	return m
}

var eventBytesRE = regexp.MustCompile(`\{"bytes":"([A-Za-z0-9+/=]+)"\}`)

// decodedChunks returns the chunk JSON objects inside the eventstream frames.
func decodedChunks(t *testing.T, wire []byte) []string {
	t.Helper()
	var out []string
	for _, m := range eventBytesRE.FindAllSubmatch(wire, -1) {
		b, err := base64.StdEncoding.DecodeString(string(m[1]))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
	}
	return out
}

// The case that matters most: Bedrock + Claude main model. The verdict rides
// in the final message_delta of the main stream; the tap reads it there,
// records allow/server, and nothing else in the record or on the wire changes.
func TestInvokeStreamSafeguardRecordsServerVerdict(t *testing.T) {
	prov := bedrockInvokeStream{payloads: amMainPayloads(amToolOutcome("not_flagged"), true)}
	const path = "/model/claude-opus-5-5/invoke-with-response-stream"
	on := serveInvokeAutoMode(t, invokeStreamHandler, prov, "claude-opus-5-5", path, amMainBody, clientctx.DefaultMode())
	off := serveInvokeAutoMode(t, invokeStreamHandler, prov, "claude-opus-5-5", path, amMainBody, clientctx.Mode{SessionRef: true})

	requireAutoMode(t, on.done.AutoMode, clientctx.VerdictAllow, clientctx.StageServer)
	if !strings.Contains(on.line, `"auto_mode":{"verdict":"allow","stage":"server","signature":"claude-code-2.1"}`) {
		t.Fatalf("auto_mode wire shape: %s", on.line)
	}
	if off.done.AutoMode != nil || on.unparsed != 0 || off.unparsed != 0 {
		t.Fatalf("off auto_mode=%+v unparsed on=%v off=%v", off.done.AutoMode, on.unparsed, off.unparsed)
	}
	if !reflect.DeepEqual(comparable(t, on.line), comparable(t, off.line)) {
		t.Fatalf("the tap changed the record beyond auto_mode:\non  %s\noff %s", on.line, off.line)
	}
	if !bytes.Equal(on.body, off.body) {
		t.Fatal("the tap changed the response bytes")
	}
	chunks := decodedChunks(t, on.body)
	if len(chunks) != len(prov.payloads) || !strings.Contains(chunks[len(chunks)-2], `"safeguard_results":[{"type":"dangerous_tool_use"`) {
		t.Fatalf("client did not receive the safeguard_results delta: %q", chunks)
	}
	requireNoResponseText(t, on.all)
}

func TestInvokeStreamSafeguardVariants(t *testing.T) {
	const path = "/model/claude-opus-5-5/invoke-with-response-stream"
	for name, tc := range map[string]struct {
		results  string
		verdict  string
		unparsed float64
	}{
		"empty tool_uses":      {amSafeguard(`{"type":"available","tool_uses":{}}`), "", 0},
		"flagged":              {amToolOutcome("flagged"), clientctx.VerdictUnknown, 1},
		"status not available": {amSafeguard(`{"type":"unavailable"}`), clientctx.VerdictUnknown, 1},
	} {
		t.Run(name, func(t *testing.T) {
			r := serveInvokeAutoMode(t, invokeStreamHandler, bedrockInvokeStream{payloads: amMainPayloads(tc.results, true)}, "claude-opus-5-5", path, amMainBody, clientctx.DefaultMode())
			if tc.verdict == "" {
				if r.done.AutoMode != nil {
					t.Fatalf("auto_mode = %+v, want none", r.done.AutoMode)
				}
			} else {
				requireAutoMode(t, r.done.AutoMode, tc.verdict, clientctx.StageServer)
			}
			if r.unparsed != tc.unparsed {
				t.Fatalf("%s = %v, want %v", amUnparsed, r.unparsed, tc.unparsed)
			}
			requireNoResponseText(t, r.all)
		})
	}
}

// A stream without message_stop is a failed response: no verdict, no count.
func TestInvokeStreamWithoutStopRecordsNoVerdict(t *testing.T) {
	r := serveInvokeAutoMode(t, invokeStreamHandler, bedrockInvokeStream{payloads: amMainPayloads(amToolOutcome("flagged"), false)},
		"claude-opus-5-5", "/model/claude-opus-5-5/invoke-with-response-stream", amMainBody, clientctx.DefaultMode())
	if r.done.AutoMode != nil || r.unparsed != 0 {
		t.Fatalf("auto_mode=%+v unparsed=%v", r.done.AutoMode, r.unparsed)
	}
}

// The captured non-Claude block call on Bedrock invoke: the model ignored the
// stop sequence, closed the tag itself and ended on max_tokens.
func TestInvokeBlockCallMaxTokensRecordsAllow(t *testing.T) {
	reply := `{"id":"","type":"message","role":"assistant","model":"kimi-k3","content":[{"type":"text","text":"<block>no</block>"}],"stop_reason":"max_tokens","stop_sequence":null,"usage":{"input_tokens":3478,"output_tokens":64,"cache_read_input_tokens":30319}}`
	r := serveInvokeAutoMode(t, invokeCompleteHandler, bedrockInvokeReply{body: reply}, "kimi-k3", "/model/kimi-k3/invoke", amBlockBody, clientctx.DefaultMode())
	requireAutoMode(t, r.done.AutoMode, clientctx.VerdictAllow, clientctx.StageS1)
	if string(r.body) != reply || r.unparsed != 0 {
		t.Fatalf("unparsed=%v body=%s", r.unparsed, r.body)
	}
	requireNoResponseText(t, r.all)
}

func TestInvokeSeverityCallRecordsNothing(t *testing.T) {
	reply := `{"id":"","type":"message","role":"assistant","model":"kimi-k3","content":[{"type":"text","text":"<severity>5</severity>"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":7,"output_tokens":48}}`
	body := strings.Replace(amBlockBody, "</block>", "</severity>", 1)
	r := serveInvokeAutoMode(t, invokeCompleteHandler, bedrockInvokeReply{body: reply}, "kimi-k3", "/model/kimi-k3/invoke", body, clientctx.DefaultMode())
	if r.done.AutoMode != nil || r.unparsed != 0 {
		t.Fatalf("auto_mode=%+v unparsed=%v", r.done.AutoMode, r.unparsed)
	}
}

// Converse is out of scope: a safeguard-bearing chunk on the Converse
// ingress records nothing.
func TestConverseStreamRecordsNoAutoMode(t *testing.T) {
	in, out := int64(10), int64(3)
	c := &nativeCapture{events: []*providers.StreamEvent{
		converseEvent(t, "contentBlockDelta", &schema.ChatChunk{Type: "content_block_delta"}),
		converseEvent(t, "messageDelta", &schema.ChatChunk{Type: "message_delta", Delta: json.RawMessage(`{"safeguard_results":` + amToolOutcome("flagged") + `}`)}),
		converseEvent(t, "messageStop", &schema.ChatChunk{Type: "message_stop"}),
		converseEvent(t, "metadata", &schema.ChatChunk{Usage: &schema.Usage{InputTokens: &in, OutputTokens: &out}}),
	}}
	converse := func(r *router.Router, h *live.Holder, w *audit.Writer, m *metrics.Metrics) http.Handler {
		return NewConverseHandlerMetrics(r, h, w, nil, m, true)
	}
	r := serveInvokeAutoMode(t, converse, c, "native", "/model/native/converse-stream", invokeBody, clientctx.DefaultMode())
	if r.done.AutoMode != nil || r.unparsed != 0 {
		t.Fatalf("auto_mode=%+v unparsed=%v", r.done.AutoMode, r.unparsed)
	}
}
