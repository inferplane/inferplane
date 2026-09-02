package anthropicapi

import (
	"encoding/json"
	"fmt"

	"github.com/inferplane/inferplane/internal/filter"
)

// maskBody applies a RequestFilter to the TEXT of an Anthropic-ingress request
// body and returns the masked bytes + the redaction count (ADR-009 T3). It masks
// only `messages[].content` — the string form, and the `text` field of `text`
// blocks. It NEVER touches `system` (spec §302), `tool_use`/`tool_result`,
// `thinking`/`redacted_thinking`, `cache_control`, or any other field: those are
// re-emitted verbatim. The masked body is semantically equivalent JSON (key
// order may change, which is irrelevant — masked traffic has already abandoned
// verbatim/cache-safe forwarding). On any malformed-JSON error it returns the
// error so the caller can fail closed (never forward unmasked).
// maskBody is the count-only view of maskBodyDetail, kept for callers that
// need no kind breakdown (count_tokens).
func maskBody(raw []byte, f filter.RequestFilter) ([]byte, int, error) {
	out, det, err := maskBodyDetail(raw, f)
	return out, det.Redactions, err
}

// maskBodyDetail is maskBody plus detector evidence (strategy Phase 2): the
// same pass, returning the typed filter.Detection (count + per-kind
// breakdown when the filter reports kinds) for the audit record.
func maskBodyDetail(raw []byte, f filter.RequestFilter) ([]byte, filter.Detection, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, filter.Detection{}, fmt.Errorf("maskBody: %w", err)
	}
	msgsRaw, ok := top["messages"]
	if !ok {
		return raw, filter.Detection{}, nil // nothing to mask
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return nil, filter.Detection{}, fmt.Errorf("maskBody messages: %w", err)
	}

	var det filter.Detection
	for i, mRaw := range msgs {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(mRaw, &msg); err != nil {
			return nil, det, fmt.Errorf("maskBody message[%d]: %w", i, err)
		}
		content, ok := msg["content"]
		if !ok {
			continue
		}
		masked, d, err := maskContent(content, f)
		if err != nil {
			return nil, det, err
		}
		if d.Redactions > 0 {
			msg["content"] = masked
			remarshaled, err := json.Marshal(msg)
			if err != nil {
				return nil, det, fmt.Errorf("maskBody remarshal message[%d]: %w", i, err)
			}
			msgs[i] = remarshaled
			det.Add(d)
		}
	}
	if det.Redactions == 0 {
		return raw, det, nil // unchanged — caller may still choose to forward verbatim
	}
	newMsgs, err := json.Marshal(msgs)
	if err != nil {
		return nil, det, fmt.Errorf("maskBody remarshal messages: %w", err)
	}
	top["messages"] = newMsgs
	out, err := json.Marshal(top)
	if err != nil {
		return nil, det, fmt.Errorf("maskBody remarshal: %w", err)
	}
	return out, det, nil
}

// maskContent masks a message's content, which is either a JSON string or an
// array of content blocks. For the array form only `text` blocks have their
// `text` field masked; every other block (tool_use/tool_result/thinking/…) and
// every other field (incl. cache_control) is preserved verbatim.
func maskContent(content json.RawMessage, f filter.RequestFilter) (json.RawMessage, filter.Detection, error) {
	// string form
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		masked, d := filter.Detect(f, s)
		if d.Redactions == 0 {
			return content, d, nil
		}
		b, err := json.Marshal(masked)
		return b, d, err
	}
	// array-of-blocks form
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, filter.Detection{}, fmt.Errorf("maskContent: %w", err)
	}
	var det filter.Detection
	for i, bRaw := range blocks {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(bRaw, &block); err != nil {
			return nil, det, fmt.Errorf("maskContent block[%d]: %w", i, err)
		}
		var typ string
		_ = json.Unmarshal(block["type"], &typ)
		if typ != "text" {
			continue // tool_use / tool_result / thinking / … untouched
		}
		var text string
		if err := json.Unmarshal(block["text"], &text); err != nil {
			continue // non-string text field — leave the block alone
		}
		masked, d := filter.Detect(f, text)
		if d.Redactions == 0 {
			continue
		}
		mb, err := json.Marshal(masked)
		if err != nil {
			return nil, det, err
		}
		block["text"] = mb // preserves cache_control and any sibling fields
		nb, err := json.Marshal(block)
		if err != nil {
			return nil, det, err
		}
		blocks[i] = nb
		det.Add(d)
	}
	if det.Redactions == 0 {
		return content, det, nil
	}
	out, err := json.Marshal(blocks)
	return out, det, err
}
