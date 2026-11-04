package providers

import (
	"encoding/json"
	"regexp"
	"strconv"
)

// Anchored context-overflow wordings that carry numeric evidence. Only these
// exact shapes are recognized; suffixes, unknown wording and provider text
// never cross protocols.
var (
	// Anthropic, including Claude on Bedrock InvokeModel and Converse.
	anthropicOverflow = regexp.MustCompile(`(?i)^prompt is too long: ([0-9]+) tokens > ([0-9]+) maximum$`)
	// vLLM v0.8.5 serving_engine.py context validation (Chat Completions).
	vllmMessagesOverflow   = regexp.MustCompile(`^This model's maximum context length is ([0-9]+) tokens\. However, you requested ([0-9]+) tokens in the messages, Please reduce the length of the messages\.$`)
	vllmCompletionOverflow = regexp.MustCompile(`^This model's maximum context length is ([0-9]+) tokens\. However, you requested ([0-9]+) tokens \(([0-9]+) in the messages, ([0-9]+) in the completion\)\. Please reduce the length of the messages or completion\.$`)
	// The serving engine behind Bedrock Converse for open-weight models
	// (observed 2026-10-27, openai.gpt-oss-120b-1:0).
	inputLengthOverflow = regexp.MustCompile(`^Input length \(([0-9]+)\) exceeds model's maximum context length \(([0-9]+)\)\.$`)
)

// ContextOverflowCounts returns the validated input total and limit of a
// recognized context-overflow message. The total must exceed a positive
// limit; a reported messages/completion split must add up exactly.
func ContextOverflowCounts(message string) (tokens, maximum int64, ok bool) {
	var values []int64
	tokensFirst := true
	for _, pattern := range []*regexp.Regexp{anthropicOverflow, inputLengthOverflow, vllmMessagesOverflow, vllmCompletionOverflow} {
		match := pattern.FindStringSubmatch(message)
		if match == nil {
			continue
		}
		tokensFirst = pattern == anthropicOverflow || pattern == inputLengthOverflow
		for _, raw := range match[1:] {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				return 0, 0, false
			}
			values = append(values, value)
		}
		break
	}
	if values == nil {
		return 0, 0, false
	}
	if tokensFirst {
		tokens, maximum = values[0], values[1]
	} else {
		maximum, tokens = values[0], values[1]
	}
	if tokens <= maximum {
		return 0, 0, false
	}
	// Validate the provider's split without overflowing addition.
	if len(values) == 4 && (values[2] >= tokens || tokens-values[2] != values[3]) {
		return 0, 0, false
	}
	return tokens, maximum, true
}

// PromptTooLongBody is the recoverable overflow error every ingress already
// understands: Anthropic's envelope with error.code context_length_exceeded
// (Responses and Chat clients read the code) and Claude Code's "prompt is too
// long" wording, with the validated counts when known (tokens > 0).
func PromptTooLongBody(tokens, maximum int64) []byte {
	message := "prompt is too long"
	if tokens > 0 && maximum > 0 {
		message += ": " + strconv.FormatInt(tokens, 10) + " tokens > " + strconv.FormatInt(maximum, 10) + " maximum"
	}
	body, _ := json.Marshal(struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Type: "error", Error: struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Type: "invalid_request_error", Code: "context_length_exceeded", Message: message}})
	return body
}
