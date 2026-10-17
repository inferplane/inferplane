package sensitivity

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestSegmentsFollowTheInspectorVisitOrder(t *testing.T) {
	raw := []byte(`{"system":"sys","messages":[{"role":"user","content":"hello 김민수"}],"max_tokens":32}`)
	got, err := Segments(context.Background(), "anthropic", raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"system", "sys", "messages", "role", "user", "content", "hello 김민수", "max_tokens", "32"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

// Review Focus 2 and PR-08: every retransmitted position is classifier input,
// including system input, tool schemas, assistant history, tool calls and results.
func TestSegmentsCoverEveryProtectedPosition(t *testing.T) {
	for _, tc := range []struct {
		protocol, body string
		marks          []string
	}{
		{"anthropic", `{"system":[{"type":"text","text":"SYSTEM-MARK"}],"tools":[{"name":"read_file","description":"TOOL-DESC-MARK","input_schema":{"type":"object","properties":{"path":{"type":"string","description":"SCHEMA-MARK"}}}}],"messages":[{"role":"user","content":"first question"},{"role":"assistant","content":[{"type":"text","text":"HISTORY-MARK"},{"type":"tool_use","id":"t1","name":"read_file","input":{"path":"ARG-MARK"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"RESULT-MARK"}]}],"max_tokens":64}`,
			[]string{"SYSTEM-MARK", "TOOL-DESC-MARK", "SCHEMA-MARK", "HISTORY-MARK", "ARG-MARK", "RESULT-MARK"}},
		{"openai", `{"messages":[{"role":"system","content":"SYSTEM-MARK"},{"role":"user","content":"q"},{"role":"assistant","content":"HISTORY-MARK","tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{\"name\":\"\\uae40\\ubbfc\\uc218\"}"}}]},{"role":"tool","tool_call_id":"c1","content":"RESULT-MARK"}],"tools":[{"type":"function","function":{"name":"lookup","description":"TOOL-DESC-MARK","parameters":{"type":"object","properties":{}}}}]}`,
			[]string{"SYSTEM-MARK", "HISTORY-MARK", "김민수", "RESULT-MARK", "TOOL-DESC-MARK"}},
		{"responses", `{"model":"m","instructions":"SYSTEM-MARK","tools":[{"type":"function","name":"lookup","description":"TOOL-DESC-MARK","parameters":{"type":"object","properties":{}}}],"input":[{"role":"user","content":[{"type":"input_text","text":"q"}]},{"role":"assistant","content":[{"type":"output_text","text":"HISTORY-MARK"}]},{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{\"q\":\"ARG-MARK\"}"},{"type":"function_call_output","call_id":"c1","output":"RESULT-MARK"}]}`,
			[]string{"SYSTEM-MARK", "TOOL-DESC-MARK", "HISTORY-MARK", "ARG-MARK", "RESULT-MARK"}},
		{"bedrock", `{"anthropic_version":"bedrock-2023-05-31","system":"SYSTEM-MARK","max_tokens":64,"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"HISTORY-MARK"},{"role":"user","content":"RESULT-MARK"}]}`,
			[]string{"SYSTEM-MARK", "HISTORY-MARK", "RESULT-MARK"}},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			if _, err := NewInspector().Inspect(context.Background(), tc.protocol, []byte(tc.body)); err != nil {
				t.Fatalf("fixture rejected by Inspect (fix the fixture, not Segments): %v", err)
			}
			got, err := Segments(context.Background(), tc.protocol, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(got, "\n")
			for _, mark := range tc.marks {
				if !strings.Contains(joined, mark) {
					t.Errorf("segments miss %s", mark)
				}
			}
		})
	}
}

func TestSegmentsFailWhereInspectFails(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx      context.Context
		protocol string
		body     string
	}{
		"malformed":     {context.Background(), "anthropic", `{"messages":[`},
		"duplicate key": {context.Background(), "anthropic", `{"messages":[],"messages":[]}`},
		"protocol":      {context.Background(), "gemini", `{"messages":[]}`},
		"cancelled":     {cancelled, "anthropic", `{"messages":[]}`},
		"budget":        {context.Background(), "anthropic", `{"messages":[{"role":"user","content":"x"}],"max_tokens":"lots"}`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewInspector().Inspect(tc.ctx, tc.protocol, []byte(tc.body)); err == nil {
				t.Fatal("Inspect accepted the case; the case is wrong")
			}
			if got, err := Segments(tc.ctx, tc.protocol, []byte(tc.body)); err == nil {
				t.Fatalf("Segments accepted it: %q", got)
			}
		})
	}
}
