package anthropicapi

import (
	"strings"
	"testing"
)

// stubMasker masks every occurrence of "PII" → "X" and counts them — enough to
// assert WHICH text spans the body masker feeds to a filter (structure scoping),
// independent of the real detectors.
type stubMasker struct{}

func (stubMasker) Name() string { return "stub" }
func (stubMasker) Mask(t string) (string, int) {
	n := strings.Count(t, "PII")
	return strings.ReplaceAll(t, "PII", "X"), n
}

func TestMaskBodyStringContent(t *testing.T) {
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":"call PII now"}]}`)
	out, n, err := maskBody(raw, stubMasker{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || !strings.Contains(string(out), `"call X now"`) {
		t.Fatalf("string content not masked: %s (n=%d)", out, n)
	}
	if strings.Contains(string(out), "PII") {
		t.Fatalf("PII survived: %s", out)
	}
}

func TestMaskBodyTextBlockOnly_ToolUntouched(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"assistant","content":[` +
		`{"type":"text","text":"see PII here"},` +
		`{"type":"tool_use","id":"t1","name":"f","input":{"q":"PII inside tool"}}` +
		`]}]}`)
	out, n, err := maskBody(raw, stubMasker{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if n != 1 || !strings.Contains(s, `"see X here"`) {
		t.Fatalf("text block not masked: %s (n=%d)", s, n)
	}
	// tool_use input must be untouched — PII inside it survives verbatim.
	if !strings.Contains(s, "PII inside tool") {
		t.Fatalf("masker descended into tool_use (must not): %s", s)
	}
}

func TestMaskBodySystemUntouched(t *testing.T) {
	raw := []byte(`{"system":"admin note PII keep","messages":[{"role":"user","content":"hi PII"}]}`)
	out, n, err := maskBody(raw, stubMasker{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "admin note PII keep") {
		t.Fatalf("system prompt was modified (spec §302 forbids): %s", s)
	}
	if n != 1 || !strings.Contains(s, `"hi X"`) {
		t.Fatalf("message not masked: %s (n=%d)", s, n)
	}
}

func TestMaskBodyPreservesCacheControl(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"PII","cache_control":{"type":"ephemeral"}}]}]}`)
	out, _, err := maskBody(raw, stubMasker{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("cache_control not preserved on the masked block: %s", s)
	}
	if !strings.Contains(s, `"text":"X"`) {
		t.Fatalf("text not masked alongside preserved cache_control: %s", s)
	}
}

func TestMaskBodyInvalidJSON(t *testing.T) {
	if _, _, err := maskBody([]byte(`{not json`), stubMasker{}); err == nil {
		t.Fatal("invalid JSON must error (caller fails closed)")
	}
}

func TestMaskBodyNoMessagesNoOp(t *testing.T) {
	raw := []byte(`{"model":"m"}`)
	out, n, err := maskBody(raw, stubMasker{})
	if err != nil || n != 0 {
		t.Fatalf("no-messages body: n=%d err=%v", n, err)
	}
	_ = out
}

// Both legacy failures are one refusal. The handler parses its body strictly
// before masking, so neither is reachable through it; the helper pins them.
func TestLegacyMaskRefusesUnmaskableOrUnreadableBody(t *testing.T) {
	for name, raw := range map[string]string{
		"unmaskable": `{"model":"m","messages":"not-an-array"}`,
		"unreadable": `{"model":"m","max_tokens":"16","messages":[{"role":"user","content":"PII"}]}`,
	} {
		if _, _, _, err := legacyMask([]byte(raw), stubMasker{}); err == nil {
			t.Fatalf("%s body was accepted", name)
		}
	}
	body, parsed, n, err := legacyMask([]byte(`{"model":"m","messages":[{"role":"user","content":"PII"}]}`), stubMasker{})
	if err != nil || n != 1 || parsed == nil || strings.Contains(string(body), "PII") || *parsed.Messages[0].Content[0].Text != "X" {
		t.Fatalf("masked body = %s parsed = %+v n = %d err = %v", body, parsed, n, err)
	}
	clean := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	body, parsed, n, err = legacyMask([]byte(clean), stubMasker{})
	if err != nil || n != 0 || parsed != nil || string(body) != clean {
		t.Fatalf("clean body = %s parsed = %+v n = %d err = %v", body, parsed, n, err)
	}
}
