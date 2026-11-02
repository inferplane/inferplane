package bedrock

import (
	"encoding/json"
	"strings"
	"testing"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// Measured 2026-10-27: Claude Code's ToolSearch result on a Converse
// model became an empty tool result, so the model could conclude the search
// found nothing even though the tool definitions arrived on the next turn.
func TestToolResultTextNamesLoadedTools(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"references only", `[{"type":"tool_reference","tool_name":"CronList"},{"type":"tool_reference","tool_name":"mcp__probe__tool-17"}]`, "Loaded tool: CronList\nLoaded tool: mcp__probe__tool-17"},
		{"text then reference", `[{"type":"text","text":"Found 1 tool."},{"type":"tool_reference","tool_name":"CronList"}]`, "Found 1 tool.\nLoaded tool: CronList"},
		{"reference then text", `[{"type":"tool_reference","tool_name":"CronList"},{"type":"text","text":"Found"}]`, "Loaded tool: CronList\nFound"},
		{"reference text reference", `[{"type":"tool_reference","tool_name":"A"},{"type":"text","text":"x"},{"type":"tool_reference","tool_name":"B"}]`, "Loaded tool: A\nx\nLoaded tool: B"},
		{"control characters are stripped", `[{"type":"tool_reference","tool_name":"a\nb\rc\u0000d\u0085e"}]`, "Loaded tool: abcde"},
		{"invalid utf-8 is dropped", "[{\"type\":\"tool_reference\",\"tool_name\":\"a\xffb\"}]", "Loaded tool: ab"},
		{"name empty after cleaning is skipped", `[{"type":"tool_reference","tool_name":"\n\u0000"}]`, ""},
		{"over-long name is capped on a rune boundary", `[{"type":"tool_reference","tool_name":"` + strings.Repeat("é", 200) + `"}]`, "Loaded tool: " + strings.Repeat("é", 128)},
		{"nameless or invalid references are skipped", `[{"type":"tool_reference"},{"type":"tool_reference","tool_name":7},{"type":"tool_reference","tool_name":""}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolResultText(json.RawMessage(tc.content)); got != tc.want {
				t.Fatalf("toolResultText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConverseToolSearchHistoryKeepsReferencesAndDropsThinking(t *testing.T) {
	cr, err := toConverseRequest([]byte(`{"messages":[
		{"role":"user","content":"schedule a job"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"use search","signature":"c2ln"},{"type":"redacted_thinking","data":"ZW5j"},{"type":"tool_use","id":"t1","name":"ToolSearch","input":{"query":"cron"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"CronList"}]}]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	msgs := buildMessages(cr.Messages)
	if len(msgs) != 3 || len(msgs[1].Content) != 1 {
		t.Fatalf("thinking must be dropped and tool_use kept: %+v", msgs)
	}
	if _, ok := msgs[1].Content[0].(*brtypes.ContentBlockMemberToolUse); !ok {
		t.Fatalf("assistant turn lost its tool_use: %T", msgs[1].Content[0])
	}
	result, ok := msgs[2].Content[0].(*brtypes.ContentBlockMemberToolResult)
	if !ok {
		t.Fatalf("tool result missing: %T", msgs[2].Content[0])
	}
	text, ok := result.Value.Content[0].(*brtypes.ToolResultContentBlockMemberText)
	if !ok || text.Value != "Loaded tool: CronList" {
		t.Fatalf("tool search result = %+v, want the loaded tool named", result.Value.Content)
	}
}
