package clientctx

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/pkg/schema"
)

// Auto mode evidence values (ADR-057 decision 4). The verdict is an estimate
// read from an Anthropic-format response; it never changes the response.
const (
	// AutoModeSignature names the client response format these rules follow.
	AutoModeSignature = "claude-code-2.1"

	VerdictBlock   = "block"
	VerdictAllow   = "allow"
	VerdictUnknown = "unknown"

	// StageS1 is a block call (request stop sequences contain </block>),
	// StageS2 the auxiliary-only full text rule, StageServer the server-side
	// safeguard carried in the response itself.
	StageS1     = "s1"
	StageS2     = "s2"
	StageServer = "server"

	maxAutoModeText = 16 << 10
	blockStop       = "</block>"
	severityStop    = "</severity>"
	safeguardType   = "dangerous_tool_use"
)

var (
	// An unclosed <thinking> hides everything after it.
	thinkingRE = regexp.MustCompile(`(?s)<thinking>.*?(?:</thinking>|\z)`)
	// Group 1 is the value; group 2 is set when </block> closes it.
	blockRE = regexp.MustCompile(`<block>\s*(yes|no)\b(\s*</block>)?`)
	// A reply ending in a severity tag carries no block decision.
	severityEndRE = regexp.MustCompile(`<severity>[^<]*(?:</severity>)?\s*\z`)
)

// AutoModeTap estimates one response's Claude Code auto mode verdict. It
// buffers at most 16 KiB of assistant text, and only for a block call or an
// auxiliary request; for every other request it reads only the structured
// safeguard_results field. It stores no text in its result. A nil tap ignores
// everything and reports nothing.
type AutoModeTap struct {
	blockCall     bool // request stop sequences contain </block>
	onlyBlockStop bool // </block> is the only requested stop sequence
	severityCall  bool // request stops on </severity> and is not a block call
	auxiliary     bool // x-claude-code-request-class: auxiliary

	text                     strings.Builder
	truncated                bool
	stopReason, stopSequence string

	safeguard string // "", VerdictAllow or VerdictUnknown
}

// NewAutoModeTap returns a tap for one request, given its stop sequences and
// whether the client declared it auxiliary.
func NewAutoModeTap(requestStops []string, auxiliary bool) *AutoModeTap {
	block := slices.Contains(requestStops, blockStop)
	only := block && !slices.ContainsFunc(requestStops, func(s string) bool { return s != blockStop })
	return &AutoModeTap{
		blockCall:     block,
		onlyBlockStop: only,
		severityCall:  !block && slices.Contains(requestStops, severityStop),
		auxiliary:     auxiliary,
	}
}

// AutoModeTapFor returns the tap for one Anthropic-format request, or nil
// when auto mode evidence is off for this request.
func AutoModeTapFor(ctx context.Context, req *schema.ChatRequest) *AutoModeTap {
	if !AutoModeWanted(ctx) {
		return nil
	}
	c := audit.ClientFrom(ctx)
	return NewAutoModeTap(RequestStopSequences(req), c != nil && c.RequestClass == ClassAuxiliary)
}

func (t *AutoModeTap) readsText() bool {
	return !t.severityCall && (t.blockCall || t.auxiliary)
}

func (t *AutoModeTap) addText(s string) {
	room := maxAutoModeText - t.text.Len()
	if room <= 0 {
		t.truncated = t.truncated || s != ""
		return
	}
	if len(s) > room {
		s, t.truncated = s[:room], true
	}
	t.text.WriteString(s)
}

// ObserveResponse reads a complete Anthropic-format response.
func (t *AutoModeTap) ObserveResponse(r *schema.ChatResponse) {
	if t == nil || r == nil || t.severityCall {
		return
	}
	if t.readsText() {
		for _, b := range r.Content {
			if b.Type == "text" && b.Text != nil {
				t.addText(*b.Text)
			}
		}
		if r.StopReason != nil {
			t.stopReason = *r.StopReason
		}
		if r.StopSequence != nil {
			t.stopSequence = *r.StopSequence
		}
	}
	if raw, ok := r.Extra["safeguard_results"]; ok {
		t.observeSafeguards(raw)
	}
}

// ObserveChunk reads one canonical Anthropic stream chunk. Its Delta is the
// upstream delta object verbatim, so safeguard_results on message_delta
// survives canonicalization.
func (t *AutoModeTap) ObserveChunk(c *schema.ChatChunk) {
	if t == nil || c == nil || len(c.Delta) == 0 || t.severityCall {
		return
	}
	switch c.Type {
	case "content_block_delta":
		if !t.readsText() {
			return
		}
		var d struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(c.Delta, &d) == nil && d.Type == "text_delta" {
			t.addText(d.Text)
		}
	case "message_delta":
		var d struct {
			StopReason       *string         `json:"stop_reason"`
			StopSequence     *string         `json:"stop_sequence"`
			SafeguardResults json.RawMessage `json:"safeguard_results"`
		}
		if json.Unmarshal(c.Delta, &d) != nil {
			return
		}
		if t.readsText() {
			if d.StopReason != nil {
				t.stopReason = *d.StopReason
			}
			if d.StopSequence != nil {
				t.stopSequence = *d.StopSequence
			}
		}
		t.observeSafeguards(d.SafeguardResults)
	}
}

// observeSafeguards folds one safeguard_results array: any unknown wins over
// allow, and allow over nothing.
func (t *AutoModeTap) observeSafeguards(raw json.RawMessage) {
	switch safeguardVerdict(raw) {
	case VerdictUnknown:
		t.safeguard = VerdictUnknown
	case VerdictAllow:
		if t.safeguard == "" {
			t.safeguard = VerdictAllow
		}
	}
}

// safeguardVerdict reads only structured fields: a dangerous_tool_use entry
// whose status is available and whose every tool use is evaluated and
// not_flagged is allow; any other status, tool-use type or outcome, or a
// shape that does not decode, is unknown; an entry with no tool uses, or no
// dangerous_tool_use entry at all, is nothing.
func safeguardVerdict(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var entries []json.RawMessage
	if json.Unmarshal(trimmed, &entries) != nil {
		return VerdictUnknown
	}
	verdict := ""
	for _, e := range entries {
		var entry struct {
			Type   string          `json:"type"`
			Status json.RawMessage `json:"status"`
		}
		if json.Unmarshal(e, &entry) != nil {
			return VerdictUnknown
		}
		if entry.Type != safeguardType {
			continue
		}
		var status struct {
			Type     string                     `json:"type"`
			ToolUses map[string]json.RawMessage `json:"tool_uses"`
		}
		if json.Unmarshal(entry.Status, &status) != nil || status.Type != "available" {
			return VerdictUnknown
		}
		for _, raw := range status.ToolUses {
			var use struct {
				Type    string `json:"type"`
				Outcome string `json:"outcome"`
			}
			if json.Unmarshal(raw, &use) != nil || use.Type != "evaluated" || use.Outcome != "not_flagged" {
				return VerdictUnknown
			}
		}
		if len(status.ToolUses) > 0 {
			verdict = VerdictAllow
		}
	}
	return verdict
}

// Result returns the verdict to record, or nil, and whether it counts as an
// unparsed block call or server safeguard. Precedence: block call, then the
// auxiliary full text rule, then the server safeguard. Severity calls, and
// severity replies on block calls or auxiliary requests, are never recorded
// or counted, whatever else the response carries.
func (t *AutoModeTap) Result() (ref *audit.AutoModeRef, unparsed bool) {
	if t == nil || t.severityCall {
		return nil, false
	}
	var matches [][]string
	if t.readsText() && !t.truncated {
		visible := thinkingRE.ReplaceAllString(t.text.String(), "")
		matches = blockRE.FindAllStringSubmatch(visible, -1)
		if len(matches) == 0 && severityEndRE.MatchString(visible) {
			return nil, false // no block decision; the threshold is unknown
		}
	}
	if t.blockCall {
		v := t.blockCallVerdict(matches)
		return autoModeRef(v, StageS1), v == VerdictUnknown
	}
	if t.auxiliary && strings.Contains(t.text.String(), "<block>") {
		return autoModeRef(t.auxiliaryVerdict(matches), StageS2), false
	}
	if t.safeguard != "" {
		return autoModeRef(t.safeguard, StageServer), t.safeguard == VerdictUnknown
	}
	return nil, false
}

func autoModeRef(verdict, stage string) *audit.AutoModeRef {
	return &audit.AutoModeRef{Verdict: verdict, Stage: stage, Signature: AutoModeSignature}
}

// blockCallVerdict: given the <block> matches outside <thinking>, exactly one
// consistent yes|no, accepted when the reply stopped on </block> or the text
// closes </block> after the value. Anything else, including truncation, is
// unknown.
func (t *AutoModeTap) blockCallVerdict(matches [][]string) string {
	if t.truncated || len(matches) == 0 {
		return VerdictUnknown
	}
	verdict, closed := consistentBlock(matches)
	if verdict == "" || (!t.stoppedOnBlock() && !closed) {
		return VerdictUnknown
	}
	return verdict
}

// stoppedOnBlock reports a stop on </block>. The gateway's Converse
// translation reports stop_reason stop_sequence without naming the sequence
// (stop_sequence null), which identifies </block> only when it is the sole
// requested stop; with several stops that reply stays ambiguous.
func (t *AutoModeTap) stoppedOnBlock() bool {
	if t.stopReason != "stop_sequence" {
		return false
	}
	return t.stopSequence == blockStop || (t.stopSequence == "" && t.onlyBlockStop)
}

// auxiliaryVerdict is the full text rule for a declared auxiliary request
// whose reply mentions <block>: exactly one consistent value outside
// <thinking>, no stop sequence required.
func (t *AutoModeTap) auxiliaryVerdict(matches [][]string) string {
	if t.truncated || len(matches) == 0 {
		return VerdictUnknown
	}
	if verdict, _ := consistentBlock(matches); verdict != "" {
		return verdict
	}
	return VerdictUnknown
}

// consistentBlock returns the single value all matches agree on ("" when
// they conflict) and whether every match is closed by </block>.
func consistentBlock(matches [][]string) (verdict string, closed bool) {
	closed = true
	for _, m := range matches {
		v := VerdictAllow
		if m[1] == "yes" {
			v = VerdictBlock
		}
		if verdict != "" && verdict != v {
			return "", false
		}
		verdict = v
		closed = closed && m[2] != ""
	}
	return verdict, closed
}

// RequestStopSequences reads stop_sequences, which the canonical request
// keeps in Extra (Messages and Bedrock InvokeModel bodies use the same key).
func RequestStopSequences(req *schema.ChatRequest) []string {
	if req == nil {
		return nil
	}
	raw, ok := req.Extra["stop_sequences"]
	if !ok {
		return nil
	}
	var out []string
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}
