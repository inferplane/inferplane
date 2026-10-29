package anthropicapi

import (
	"context"
	"encoding/json"
	"iter"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/clientctx"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

// Shapes follow a Claude Code 2.1.312 capture (spec "Early verification
// results"); the tool-use id is a placeholder. The Messages-ingress stream
// safeguard is inferred: it was observed on Bedrock invoke-with-response-stream.

const (
	amToolUse    = "toolu_bdrk_01Fixture00000000000000"
	amUnparsed   = "inferplane_audit_auto_mode_unparsed_total"
	amBlockCall  = `{"model":"claude-sonnet-4-6","max_tokens":64,"stop_sequences":["</block>"],"messages":[{"role":"user","content":"classify"}]}`
	amStreamCall = `{"model":"claude-sonnet-4-6","max_tokens":64,"stream":true,"stop_sequences":["</block>"],"messages":[{"role":"user","content":"classify"}]}`
	amMainStream = `{"model":"claude-sonnet-4-6","max_tokens":128000,"stream":true,"messages":[{"role":"user","content":"run ls"}]}`
)

func amReply(text, stopReason, stopSeq string) string {
	b, _ := json.Marshal(text)
	sr, ss := "null", "null"
	if stopReason != "" {
		sr = `"` + stopReason + `"`
	}
	if stopSeq != "" {
		ss = `"` + stopSeq + `"`
	}
	return `{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":` + string(b) +
		`}],"stop_reason":` + sr + `,"stop_sequence":` + ss + `,"usage":{"input_tokens":158,"output_tokens":7}}`
}

// replyProvider answers Complete with a fixed Anthropic body and Stream with
// fixed chunk frames, each parsed into the canonical chunk the way the
// Anthropic SSE reader does.
type replyProvider struct {
	body   string
	frames []string
}

func (replyProvider) Name() string               { return "reply" }
func (replyProvider) Models() []schema.ModelInfo { return nil }
func (p replyProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	var r schema.ChatResponse
	if err := json.Unmarshal([]byte(p.body), &r); err != nil {
		return nil, err
	}
	return &providers.ProxyResponse{StatusCode: 200, RawBody: []byte(p.body), Parsed: &r}, nil
}
func (p replyProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return func(yield func(*providers.StreamEvent, error) bool) {
		for _, f := range p.frames {
			var c schema.ChatChunk
			if err := json.Unmarshal([]byte(f), &c); err != nil {
				yield(nil, err)
				return
			}
			var b strings.Builder
			_ = schema.WriteAnthropicSSE(&b, &c)
			if !yield(&providers.StreamEvent{Raw: []byte(b.String()), Chunk: &c}, nil) {
				return
			}
		}
	}, nil
}

type amRun struct {
	body     string // client response body
	done     audit.Record
	line     string // the terminal audit line as written
	all      string // every audit line
	unparsed float64
}

// serveAutoMode runs one Messages request through the real evidence
// middleware with the given class header (empty = absent) and mode.
func serveAutoMode(t *testing.T, prov providers.Provider, body, class string, mode clientctx.Mode) amRun {
	t.Helper()
	w, buf := evidenceWriter(t)
	m := metrics.New()
	models := map[string]config.ModelConfig{"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}}}}
	h := NewMessagesHandlerMetrics(router.New(holderFor(map[string]providers.Provider{"p": prov}, models)), w, nil, m)
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	if class != "" {
		req.Header.Set("x-claude-code-request-class", class)
	}
	req.Header.Set("X-Claude-Code-Session-Id", rawSession)
	p := keystore.Principal{KeyID: "ik_am", Team: "t", AllowedModels: []string{"*"}}
	rec := httptest.NewRecorder()
	clientctx.Middleware(mode, h).ServeHTTP(rec, req.WithContext(principal.With(req.Context(), p)))
	w.Close()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	out := amRun{body: rec.Body.String(), all: buf.String(), unparsed: gatheredCounter(t, m, amUnparsed)}
	out.done = terminal(t, records(t, buf))
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

// requireNoResponseText fails when response text, a tool-use id or the raw
// session ID reached the audit chain, raw or JSON-escaped ('<' and '>'
// marshal as < and >).
func requireNoResponseText(t *testing.T, auditText string) {
	t.Helper()
	for _, s := range []string{"<block>", "block>", "<thinking>", "<severity>", amToolUse, rawSession} {
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

// Claude Code 2.1.312 sends no request-class header; the block call is
// recognised by its </block> stop sequence alone.
func TestMessagesBlockCallRecordsVerdictWithoutClassHeader(t *testing.T) {
	reply := amReply("<block>yes", "stop_sequence", "</block>")
	r := serveAutoMode(t, replyProvider{body: reply}, amBlockCall, "", clientctx.DefaultMode())
	requireAutoMode(t, r.done.AutoMode, clientctx.VerdictBlock, clientctx.StageS1)
	if !strings.Contains(r.line, `"auto_mode":{"verdict":"block","stage":"s1","signature":"claude-code-2.1"}`) {
		t.Fatalf("auto_mode wire shape: %s", r.line)
	}
	if r.done.Client == nil || r.done.Client.RequestClass != "" || r.unparsed != 0 {
		t.Fatalf("client=%+v unparsed=%v", r.done.Client, r.unparsed)
	}
	if r.body != reply {
		t.Fatalf("response bytes changed:\n%s\nwant\n%s", r.body, reply)
	}
	requireNoResponseText(t, r.all)
}

// The captured Anthropic-mode reply: "<block>no" stopped on </block>.
func TestMessagesBlockCallAllow(t *testing.T) {
	r := serveAutoMode(t, replyProvider{body: amReply("<block>no", "stop_sequence", "</block>")}, amBlockCall, "", clientctx.DefaultMode())
	requireAutoMode(t, r.done.AutoMode, clientctx.VerdictAllow, clientctx.StageS1)
}

func TestMessagesBlockCallUnparsedCountsAndRecordsUnknown(t *testing.T) {
	r := serveAutoMode(t, replyProvider{body: amReply("<block>yes", "end_turn", "")}, amBlockCall, "", clientctx.DefaultMode())
	requireAutoMode(t, r.done.AutoMode, clientctx.VerdictUnknown, clientctx.StageS1)
	if r.unparsed != 1 {
		t.Fatalf("%s = %v, want 1", amUnparsed, r.unparsed)
	}
	requireNoResponseText(t, r.all)
}

func TestMessagesAutoModeOffRecordsNothing(t *testing.T) {
	reply := amReply("<block>yes", "end_turn", "")
	r := serveAutoMode(t, replyProvider{body: reply}, amBlockCall, "", clientctx.Mode{SessionRef: true})
	if r.done.AutoMode != nil || r.unparsed != 0 || strings.Contains(r.line, "auto_mode") {
		t.Fatalf("auto_mode=%+v unparsed=%v", r.done.AutoMode, r.unparsed)
	}
	if r.body != reply {
		t.Fatal("response bytes changed")
	}
}

func TestMessagesRequestsWithoutBlockDecisionRecordNothing(t *testing.T) {
	for name, tc := range map[string]struct{ body, reply string }{
		"main request with block text": {`{"model":"claude-sonnet-4-6","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, amReply("<block>yes</block>", "end_turn", "")},
		"severity call":                {`{"model":"claude-sonnet-4-6","max_tokens":64,"stop_sequences":["</severity>"],"messages":[{"role":"user","content":"x"}]}`, amReply("<severity>80", "stop_sequence", "</severity>")},
	} {
		t.Run(name, func(t *testing.T) {
			r := serveAutoMode(t, replyProvider{body: tc.reply}, tc.body, "", clientctx.DefaultMode())
			if r.done.AutoMode != nil || r.unparsed != 0 {
				t.Fatalf("auto_mode=%+v unparsed=%v", r.done.AutoMode, r.unparsed)
			}
		})
	}
}

// The auxiliary full text rule needs the header; with it a <thinking> reply
// without stop sequence yields stage s2.
func TestMessagesAuxiliaryClassUsesFullTextRule(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","max_tokens":8192,"messages":[{"role":"user","content":"x"}]}`
	reply := amReply("<thinking>weigh it</thinking><block>yes</block>", "end_turn", "")
	r := serveAutoMode(t, replyProvider{body: reply}, body, clientctx.ClassAuxiliary, clientctx.DefaultMode())
	requireAutoMode(t, r.done.AutoMode, clientctx.VerdictBlock, clientctx.StageS2)
	if r = serveAutoMode(t, replyProvider{body: reply}, body, "", clientctx.DefaultMode()); r.done.AutoMode != nil {
		t.Fatalf("without the header: auto_mode = %+v", r.done.AutoMode)
	}
}

func amStreamFrames(delta string, stop bool) []string {
	frames := []string{
		`{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"` + amToolUse + `","name":"Bash","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":` + delta + `,"usage":{"output_tokens":287}}`,
	}
	if stop {
		frames = append(frames, `{"type":"message_stop"}`)
	}
	return frames
}

func amSafeguardDelta(outcome string) string {
	return `{"stop_reason":"tool_use","stop_sequence":null,"stop_details":null,"container":null,"safeguard_results":[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"` +
		amToolUse + `":{"type":"evaluated","outcome":"` + outcome + `"}}}}]}`
}

func TestMessagesStreamSafeguardRecordsServerVerdict(t *testing.T) {
	r := serveAutoMode(t, replyProvider{frames: amStreamFrames(amSafeguardDelta("not_flagged"), true)}, amMainStream, "", clientctx.DefaultMode())
	requireAutoMode(t, r.done.AutoMode, clientctx.VerdictAllow, clientctx.StageServer)
	if !strings.Contains(r.body, `"safeguard_results"`) || r.unparsed != 0 {
		t.Fatalf("unparsed=%v body=%s", r.unparsed, r.body)
	}
	requireNoResponseText(t, r.all)
}

func TestMessagesStreamBlockCall(t *testing.T) {
	frames := []string{
		`{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<blo"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ck>no"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"</block>"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	}
	r := serveAutoMode(t, replyProvider{frames: frames}, amStreamCall, "", clientctx.DefaultMode())
	requireAutoMode(t, r.done.AutoMode, clientctx.VerdictAllow, clientctx.StageS1)
	requireNoResponseText(t, r.all)
}

// A stream that never completes, or completes with an error event, records
// no verdict and counts nothing, even when its safeguard did not parse.
func TestMessagesFailedStreamRecordsNoVerdict(t *testing.T) {
	for name, frames := range map[string][]string{
		"no message_stop": amStreamFrames(amSafeguardDelta("flagged"), false),
		"error event":     append(amStreamFrames(amSafeguardDelta("flagged"), false), `{"type":"error","error":{"type":"overloaded_error","message":"x"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			r := serveAutoMode(t, replyProvider{frames: frames}, amMainStream, "", clientctx.DefaultMode())
			if r.done.AutoMode != nil || r.unparsed != 0 {
				t.Fatalf("auto_mode=%+v unparsed=%v", r.done.AutoMode, r.unparsed)
			}
		})
	}
}
