package clientctx

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/pkg/schema"
)

// Fixture shapes come from a Claude Code 2.1.312 capture through a relay
// (spec "Early verification results (2026-10-29)"); ids and message ids are
// replaced. Observed: the block call replies "<block>no" stopped on
// "</block>" (Anthropic mode) and "<block>no</block>" ending max_tokens
// (Bedrock/Converse model that ignores stops); severity calls; the unstopped
// reasoning reply; the streamed safeguard_results with not_flagged and with
// empty tool_uses. Inferred, never observed: "<block>yes", any flagged or
// non-available safeguard, and a top-level safeguard_results on a complete
// (non-streamed) response.

const fixtureToolUse = "toolu_bdrk_01Fixture00000000000000"

var (
	blockStops    = []string{"</block>"}
	severityStops = []string{"</severity>"}
)

func respJSON(text, stopReason, stopSeq, extra string) string {
	b, _ := json.Marshal(text)
	sr, ss := "null", "null"
	if stopReason != "" {
		sr = `"` + stopReason + `"`
	}
	if stopSeq != "" {
		ss = `"` + stopSeq + `"`
	}
	return `{"id":"msg_fixture","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":` + string(b) +
		`}],"stop_reason":` + sr + `,"stop_sequence":` + ss + extra + `,"usage":{"input_tokens":5,"output_tokens":2}}`
}

func parsed(t *testing.T, body string) *schema.ChatResponse {
	t.Helper()
	var r schema.ChatResponse
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("fixture: %v: %s", err, body)
	}
	return &r
}

func safeguard(status string) string {
	return `[{"type":"dangerous_tool_use","status":` + status + `}]`
}

func notFlagged() string {
	return safeguard(`{"type":"available","tool_uses":{"` + fixtureToolUse + `":{"type":"evaluated","outcome":"not_flagged"}}}`)
}

func TestAutoModeVerdicts(t *testing.T) {
	long := strings.Repeat("x", 17<<10)
	cases := []struct {
		name           string
		stops          []string
		aux            bool
		body           string
		verdict, stage string // empty verdict: nothing recorded
		unparsed       bool
	}{
		// Block call (stage s1): stop sequences contain </block>.
		{"block call no, stopped on </block>", blockStops, false, respJSON("<block>no", "stop_sequence", "</block>", ""), VerdictAllow, StageS1, false},
		{"block call no</block>, max_tokens", blockStops, false, respJSON("<block>no</block>", "max_tokens", "", ""), VerdictAllow, StageS1, false},
		{"block call yes, stopped on </block>", blockStops, false, respJSON("<block>yes", "stop_sequence", "</block>", ""), VerdictBlock, StageS1, false},
		{"block call yes</block>, end_turn", blockStops, false, respJSON("<block>yes</block>", "end_turn", "", ""), VerdictBlock, StageS1, false},
		{"block call value after thinking", blockStops, false, respJSON("<thinking>maybe <block>no</block></thinking><block>yes", "stop_sequence", "</block>", ""), VerdictBlock, StageS1, false},
		{"block call conflicting values", blockStops, false, respJSON("<block>yes</block> <block>no</block>", "max_tokens", "", ""), VerdictUnknown, StageS1, true},
		{"block call no block value", blockStops, false, respJSON("I cannot decide", "end_turn", "", ""), VerdictUnknown, StageS1, true},
		{"block call value not yes or no", blockStops, false, respJSON("<block>maybe", "stop_sequence", "</block>", ""), VerdictUnknown, StageS1, true},
		{"block call value only inside thinking", blockStops, false, respJSON("<thinking><block>yes", "stop_sequence", "</block>", ""), VerdictUnknown, StageS1, true},
		{"block call wrong stop, no closing tag", blockStops, false, respJSON("<block>yes", "end_turn", "", ""), VerdictUnknown, StageS1, true},
		{"block call stopped on another sequence", blockStops, false, respJSON("<block>yes", "stop_sequence", "</severity>", ""), VerdictUnknown, StageS1, true},
		{"block call truncated past 16 KiB", blockStops, false, respJSON(long+"<block>yes</block>", "end_turn", "", ""), VerdictUnknown, StageS1, true},
		{"block call with other stops too", []string{"\n\nHuman:", "</block>"}, false, respJSON("<block>no", "stop_sequence", "</block>", ""), VerdictAllow, StageS1, false},

		// Severity calls and replies ending in <severity>: never recorded.
		{"severity call 5", severityStops, false, respJSON("<severity>5</severity>", "end_turn", "", ""), "", "", false},
		{"severity call 80 stopped", severityStops, false, respJSON("<severity>80", "stop_sequence", "</severity>", ""), "", "", false},
		{"severity call, auxiliary", severityStops, true, respJSON("<severity>80", "stop_sequence", "</severity>", ""), "", "", false},
		{"unstopped reasoning reply", nil, false, respJSON("<thinking>remote script piped to sh</thinking><severity>35</severity>", "end_turn", "", ""), "", "", false},
		{"unstopped reasoning reply, auxiliary", nil, true, respJSON("<thinking>remote script piped to sh</thinking><severity>35</severity>", "end_turn", "", ""), "", "", false},
		{"auxiliary severity after a thinking-only block", nil, true, respJSON("<thinking><block>yes</block></thinking><severity>35</severity>", "end_turn", "", ""), "", "", false},
		{"block call answered with severity only", blockStops, false, respJSON("<severity>5</severity>", "end_turn", "", ""), "", "", false},

		// Auxiliary full text rule (stage s2), header-based.
		{"auxiliary thinking then block yes", nil, true, respJSON("<thinking>maybe <block>no</block></thinking>\n<block>yes</block>", "end_turn", "", ""), VerdictBlock, StageS2, false},
		{"auxiliary block no", nil, true, respJSON("<block>no</block>", "end_turn", "", ""), VerdictAllow, StageS2, false},
		{"auxiliary block only inside thinking", nil, true, respJSON("<thinking><block>yes</block></thinking>", "end_turn", "", ""), VerdictUnknown, StageS2, false},
		{"auxiliary conflicting", nil, true, respJSON("<block>yes</block> <block>no</block>", "end_turn", "", ""), VerdictUnknown, StageS2, false},
		{"auxiliary truncated with block in prefix", nil, true, respJSON("<block>"+long, "max_tokens", "", ""), VerdictUnknown, StageS2, false},
		{"auxiliary without any block tag", nil, true, respJSON("Fix login redirect", "end_turn", "", ""), "", "", false},

		// Non-block, non-auxiliary requests read no text.
		{"main without stops", nil, false, respJSON("hello", "end_turn", "", ""), "", "", false},
		{"main text with block value", nil, false, respJSON("<block>yes</block>", "end_turn", "", ""), "", "", false},
		{"main stopped on </block> without asking", nil, false, respJSON("<block>yes", "stop_sequence", "</block>", ""), "", "", false},

		// Server safeguard (stage server), complete response (inferred shape).
		{"safeguard not_flagged", nil, false, respJSON("ok", "tool_use", "", `,"safeguard_results":`+notFlagged()), VerdictAllow, StageServer, false},
		{"safeguard empty tool_uses", nil, false, respJSON("ok", "end_turn", "", `,"safeguard_results":`+safeguard(`{"type":"available","tool_uses":{}}`)), "", "", false},
		{"safeguard flagged", nil, false, respJSON("ok", "tool_use", "", `,"safeguard_results":`+safeguard(`{"type":"available","tool_uses":{"`+fixtureToolUse+`":{"type":"evaluated","outcome":"flagged"}}}`)), VerdictUnknown, StageServer, true},
		{"safeguard other outcome", nil, false, respJSON("ok", "tool_use", "", `,"safeguard_results":`+safeguard(`{"type":"available","tool_uses":{"`+fixtureToolUse+`":{"type":"evaluated","outcome":"blocked"}}}`)), VerdictUnknown, StageServer, true},
		{"safeguard tool use not evaluated", nil, false, respJSON("ok", "tool_use", "", `,"safeguard_results":`+safeguard(`{"type":"available","tool_uses":{"`+fixtureToolUse+`":{"type":"skipped"}}}`)), VerdictUnknown, StageServer, true},
		{"safeguard one of two flagged", nil, false, respJSON("ok", "tool_use", "", `,"safeguard_results":`+safeguard(`{"type":"available","tool_uses":{"a":{"type":"evaluated","outcome":"not_flagged"},"b":{"type":"evaluated","outcome":"flagged"}}}`)), VerdictUnknown, StageServer, true},
		{"safeguard status unavailable", nil, false, respJSON("ok", "end_turn", "", `,"safeguard_results":`+safeguard(`{"type":"unavailable"}`)), VerdictUnknown, StageServer, true},
		{"safeguard status unavailable with tool uses", nil, false, respJSON("ok", "tool_use", "", `,"safeguard_results":`+safeguard(`{"type":"error","tool_uses":{"a":{"type":"evaluated","outcome":"not_flagged"}}}`)), VerdictUnknown, StageServer, true},
		{"safeguard malformed tool_uses", nil, false, respJSON("ok", "tool_use", "", `,"safeguard_results":`+safeguard(`{"type":"available","tool_uses":["a"]}`)), VerdictUnknown, StageServer, true},
		{"safeguard_results not an array", nil, false, respJSON("ok", "tool_use", "", `,"safeguard_results":{"type":"dangerous_tool_use"}`), VerdictUnknown, StageServer, true},
		{"safeguard_results null", nil, false, respJSON("ok", "end_turn", "", `,"safeguard_results":null`), "", "", false},
		{"safeguard of another type", nil, false, respJSON("ok", "tool_use", "", `,"safeguard_results":[{"type":"other_check","status":{"type":"available","tool_uses":{"a":{"type":"evaluated","outcome":"flagged"}}}}]`), "", "", false},

		// Precedence: block call, then auxiliary full rule, then server safeguard.
		{"block call wins over safeguard", blockStops, false, respJSON("<block>yes", "stop_sequence", "</block>", `,"safeguard_results":`+notFlagged()), VerdictBlock, StageS1, false},
		{"auxiliary wins over safeguard", nil, true, respJSON("<block>no</block>", "end_turn", "", `,"safeguard_results":`+safeguard(`{"type":"unavailable"}`)), VerdictAllow, StageS2, false},
		{"auxiliary without block falls to safeguard", nil, true, respJSON("done", "tool_use", "", `,"safeguard_results":`+notFlagged()), VerdictAllow, StageServer, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tap := NewAutoModeTap(c.stops, c.aux)
			tap.ObserveResponse(parsed(t, c.body))
			ref, unparsed := tap.Result()
			if c.verdict == "" {
				if ref != nil || unparsed {
					t.Fatalf("want nothing recorded, got ref=%+v unparsed=%v", ref, unparsed)
				}
				return
			}
			if ref == nil || ref.Verdict != c.verdict || ref.Stage != c.stage || ref.Signature != AutoModeSignature {
				t.Fatalf("ref = %+v, want %s/%s/%s", ref, c.verdict, c.stage, AutoModeSignature)
			}
			if unparsed != c.unparsed {
				t.Fatalf("unparsed = %v, want %v", unparsed, c.unparsed)
			}
		})
	}
}

func chunk(t *testing.T, frame string) *schema.ChatChunk {
	t.Helper()
	var c schema.ChatChunk
	if err := json.Unmarshal([]byte(frame), &c); err != nil {
		t.Fatalf("frame: %v: %s", err, frame)
	}
	return &c
}

// mainStream is the shape of a Bedrock invoke-with-response-stream main turn
// with one tool call; finalDelta is the last message_delta's delta object.
func mainStream(finalDelta string) []string {
	return []string{
		`{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Running <block>yes</block> now."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"` + fixtureToolUse + `","name":"Bash","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":` + finalDelta + `,"usage":{"input_tokens":2,"output_tokens":287}}`,
		`{"type":"message_stop"}`,
	}
}

func capturedDelta(safeguardResults string) string {
	return `{"stop_reason":"tool_use","stop_sequence":null,"stop_details":null,"container":null,"safeguard_results":` + safeguardResults + `}`
}

func TestAutoModeStreamBlockCall(t *testing.T) {
	tap := NewAutoModeTap(blockStops, false)
	for _, f := range []string{
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<blo"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ck>yes"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"</block>"}}`,
	} {
		tap.ObserveChunk(chunk(t, f))
	}
	ref, unparsed := tap.Result()
	if ref == nil || ref.Verdict != VerdictBlock || ref.Stage != StageS1 || unparsed {
		t.Fatalf("stream ref=%+v unparsed=%v", ref, unparsed)
	}
}

func TestAutoModeStreamServerSafeguard(t *testing.T) {
	cases := []struct {
		name           string
		results        string
		verdict, stage string
		unparsed       bool
	}{
		{"not_flagged", notFlagged(), VerdictAllow, StageServer, false},
		{"empty tool_uses", safeguard(`{"type":"available","tool_uses":{}}`), "", "", false},
		{"flagged", safeguard(`{"type":"available","tool_uses":{"` + fixtureToolUse + `":{"type":"evaluated","outcome":"flagged"}}}`), VerdictUnknown, StageServer, true},
		{"status other than available", safeguard(`{"type":"unavailable","tool_uses":{}}`), VerdictUnknown, StageServer, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tap := NewAutoModeTap(nil, false)
			for _, f := range mainStream(capturedDelta(c.results)) {
				tap.ObserveChunk(chunk(t, f))
			}
			if tap.text.Len() != 0 {
				t.Fatalf("a main request buffered %d bytes of response text", tap.text.Len())
			}
			ref, unparsed := tap.Result()
			if c.verdict == "" {
				if ref != nil || unparsed {
					t.Fatalf("want nothing, got %+v %v", ref, unparsed)
				}
				return
			}
			if ref == nil || ref.Verdict != c.verdict || ref.Stage != c.stage || unparsed != c.unparsed {
				t.Fatalf("ref=%+v unparsed=%v", ref, unparsed)
			}
		})
	}
}

// A main stream without safeguard_results records nothing, even when its
// text contains a block value.
func TestAutoModeStreamMainWithoutSafeguard(t *testing.T) {
	tap := NewAutoModeTap(nil, false)
	for _, f := range mainStream(`{"stop_reason":"end_turn","stop_sequence":null}`) {
		tap.ObserveChunk(chunk(t, f))
	}
	if ref, unparsed := tap.Result(); ref != nil || unparsed {
		t.Fatalf("ref=%+v unparsed=%v", ref, unparsed)
	}
}

// Two safeguard deltas in one stream: any unknown wins over allow.
func TestAutoModeSafeguardsCombine(t *testing.T) {
	tap := NewAutoModeTap(nil, false)
	tap.ObserveChunk(chunk(t, `{"type":"message_delta","delta":{"safeguard_results":`+notFlagged()+`}}`))
	tap.ObserveChunk(chunk(t, `{"type":"message_delta","delta":{"safeguard_results":`+safeguard(`{"type":"unavailable"}`)+`}}`))
	if ref, unparsed := tap.Result(); ref == nil || ref.Verdict != VerdictUnknown || !unparsed {
		t.Fatalf("ref=%+v unparsed=%v", ref, unparsed)
	}
}

// Text is read only for block calls and auxiliary requests.
func TestAutoModeTextOnlyWhenNeeded(t *testing.T) {
	for _, c := range []struct {
		stops []string
		aux   bool
		want  bool
	}{{nil, false, false}, {severityStops, false, false}, {blockStops, false, true}, {nil, true, true}} {
		tap := NewAutoModeTap(c.stops, c.aux)
		tap.ObserveResponse(parsed(t, respJSON("<block>no", "end_turn", "", "")))
		tap.ObserveChunk(chunk(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"more"}}`))
		if got := tap.text.Len() > 0; got != c.want {
			t.Fatalf("stops=%v aux=%v buffered=%v, want %v", c.stops, c.aux, got, c.want)
		}
	}
}

func TestAutoModeTextBound(t *testing.T) {
	tap := NewAutoModeTap(blockStops, false)
	for i := 0; i < 40; i++ {
		tap.ObserveChunk(chunk(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+strings.Repeat("x", 1024)+`"}}`))
	}
	if tap.text.Len() > 16<<10 {
		t.Fatalf("buffered %d bytes, bound is 16 KiB", tap.text.Len())
	}
	if ref, unparsed := tap.Result(); ref == nil || ref.Verdict != VerdictUnknown || !unparsed {
		t.Fatalf("over-bound text must be unknown: %+v %v", ref, unparsed)
	}
}

func TestRequestStopSequences(t *testing.T) {
	for body, want := range map[string]string{
		`{"model":"m","messages":[],"stop_sequences":["</block>"]}`:                                                 "</block>",
		`{"anthropic_version":"bedrock-2023-05-31","messages":[],"max_tokens":64,"stop_sequences":["</severity>"]}`: "</severity>",
		`{"model":"m","messages":[]}`:                       "",
		`{"model":"m","messages":[],"stop_sequences":"x"}`:  "",
		`{"model":"m","messages":[],"stop_sequences":null}`: "",
	} {
		var req schema.ChatRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(RequestStopSequences(&req), ","); got != want {
			t.Fatalf("%s: stops = %q, want %q", body, got, want)
		}
	}
	if RequestStopSequences(nil) != nil {
		t.Fatal("nil request must yield no stops")
	}
}

func TestNilTapIsSafe(t *testing.T) {
	var tap *AutoModeTap
	tap.ObserveResponse(parsed(t, respJSON("<block>yes", "stop_sequence", "</block>", "")))
	tap.ObserveChunk(&schema.ChatChunk{Type: "message_delta"})
	tap.ObserveResponse(nil)
	tap.ObserveChunk(nil)
	if ref, unparsed := tap.Result(); ref != nil || unparsed {
		t.Fatal("nil tap must report nothing")
	}
}

func TestAutoModeTapFor(t *testing.T) {
	req := &schema.ChatRequest{Extra: map[string]json.RawMessage{"stop_sequences": json.RawMessage(`["</block>"]`)}}
	base := audit.WithEvidence(context.Background(), time.Now(), &audit.ClientRef{RequestClass: ClassAuxiliary})
	if AutoModeTapFor(WithMode(base, Mode{SessionRef: true}), req) != nil {
		t.Fatal("auto mode off must yield no tap")
	}
	if AutoModeTapFor(base, req) != nil {
		t.Fatal("a context with no mode must yield no tap")
	}
	tap := AutoModeTapFor(WithMode(base, DefaultMode()), req)
	if tap == nil || !tap.blockCall || !tap.auxiliary {
		t.Fatalf("tap = %+v", tap)
	}
	noClass := audit.WithEvidence(context.Background(), time.Now(), nil)
	if tap := AutoModeTapFor(WithMode(noClass, DefaultMode()), &schema.ChatRequest{}); tap == nil || tap.blockCall || tap.auxiliary {
		t.Fatalf("main tap = %+v", tap)
	}
}
