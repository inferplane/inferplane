// Package responsesapi implements the OpenAI Responses API ingress
// (POST /v1/responses) — the wire current Codex (≥0.5x, wire_api
// "responses") REQUIRES for custom providers, having removed its Chat
// Completions support (openai/codex discussion #7782; verified against
// Codex 0.152.1, docs/verification/coding-agents.md).
//
// It is deliberately an ADAPTER in front of the existing Chat Completions
// ingress, not a fourth parallel pipeline: the request is translated to the
// chat wire and served by openaiapi.ChatHandler, so key auth, RBAC,
// governance (PreCheck with budget reservation / Settle), PII policy,
// routing, fallback, and the audit chain all apply automatically and can
// never drift from the other ingresses. The response (JSON or SSE) is
// translated back to the Responses shape this side of the shim. One
// consequence, documented rather than hidden: audit records for these
// requests carry ingress "openai" (the pipeline that actually served them).
//
// Translation is Codex-scoped v1: instructions → system; input items
// message / function_call / function_call_output; FLAT function tools;
// tool_choice as a plain string; max_output_tokens. Anything else the
// caller sent (reasoning, include, store, prompt_cache_key, non-function
// tools, …) is DROPPED — and disclosed on the response
// (x-inferplane-params-stripped), never silently (the strategy P1 rule).
package responsesapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/inferplane/inferplane/pkg/ulid"
)

// maxBody mirrors the chat ingress's posture of bounding reads.
const maxBody = 32 << 20

// Handler adapts POST /v1/responses onto the Chat Completions ingress.
type Handler struct {
	chat http.Handler
}

// New wraps the (fully configured) chat ingress handler.
func New(chat http.Handler) *Handler { return &Handler{chat: chat} }

// --- Responses wire (request), Codex-scoped subset ---

type responsesRequest struct {
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions,omitempty"`
	Input           json.RawMessage   `json:"input,omitempty"` // string | []inputItem
	Tools           []json.RawMessage `json:"tools,omitempty"`
	ToolChoice      json.RawMessage   `json:"tool_choice,omitempty"`
	MaxOutputTokens *int64            `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
	Stream          bool              `json:"stream,omitempty"`

	// Recognized-but-untranslatable (dropped WITH disclosure).
	Reasoning         json.RawMessage `json:"reasoning,omitempty"`
	Include           json.RawMessage `json:"include,omitempty"`
	Store             json.RawMessage `json:"store,omitempty"`
	PromptCacheKey    json.RawMessage `json:"prompt_cache_key,omitempty"`
	ParallelToolCalls json.RawMessage `json:"parallel_tool_calls,omitempty"`
	ClientMetadata    json.RawMessage `json:"client_metadata,omitempty"`
}

type inputItem struct {
	Type    string          `json:"type"` // "" (implicit message) | message | function_call | function_call_output
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"` // string | []contentPart
	// function_call fields
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	// function_call_output field: string, or structured output parts
	Output json.RawMessage `json:"output,omitempty"`
}

type contentPart struct {
	Type string `json:"type"` // input_text | output_text | refusal (ignored otherwise)
	Text string `json:"text,omitempty"`
}

type flatTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      json.RawMessage `json:"strict,omitempty"`
}

// toChatRequest translates the Responses request to a Chat Completions
// body, returning the body and the top-level params it had to drop.
func toChatRequest(rr *responsesRequest) ([]byte, []string, error) {
	var dropped []string
	msgs := []map[string]any{}
	if rr.Instructions != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": rr.Instructions})
	}

	// input: a bare string is shorthand for one user message.
	if len(rr.Input) > 0 {
		var asString string
		if err := json.Unmarshal(rr.Input, &asString); err == nil {
			msgs = append(msgs, map[string]any{"role": "user", "content": asString})
		} else {
			var items []inputItem
			if err := json.Unmarshal(rr.Input, &items); err != nil {
				return nil, nil, fmt.Errorf("input: %w", err)
			}
			for i, it := range items {
				switch it.Type {
				case "", "message":
					role := it.Role
					switch role {
					case "developer", "system":
						// The chat wire's system role; the canonical layer
						// folds it into the system prompt.
						role = "system"
					case "user", "assistant":
					default:
						return nil, nil, fmt.Errorf("input[%d]: unsupported message role %q", i, it.Role)
					}
					text, err := textOf(it.Content)
					if err != nil {
						return nil, nil, fmt.Errorf("input[%d]: %w", i, err)
					}
					msgs = append(msgs, map[string]any{"role": role, "content": text})
				case "function_call":
					// A prior assistant tool call, replayed as history.
					msgs = append(msgs, map[string]any{
						"role": "assistant",
						"tool_calls": []any{map[string]any{
							"id": it.CallID, "type": "function",
							"function": map[string]any{"name": it.Name, "arguments": it.Arguments},
						}},
					})
				case "function_call_output":
					out, err := textOf(it.Output)
					if err != nil {
						return nil, nil, fmt.Errorf("input[%d] output: %w", i, err)
					}
					msgs = append(msgs, map[string]any{
						"role": "tool", "tool_call_id": it.CallID, "content": out,
					})
				case "reasoning":
					// Encrypted/opaque reasoning items from a previous turn
					// cannot be replayed through a translating gateway.
					dropped = appendOnce(dropped, "input.reasoning")
				default:
					return nil, nil, fmt.Errorf("input[%d]: unsupported item type %q", i, it.Type)
				}
			}
		}
	}

	body := map[string]any{"model": rr.Model, "messages": msgs}
	if len(rr.Tools) > 0 {
		var tools []any
		for _, raw := range rr.Tools {
			var t flatTool
			if err := json.Unmarshal(raw, &t); err != nil {
				return nil, nil, fmt.Errorf("tools: %w", err)
			}
			if t.Type != "function" {
				// namespace / web_search / … have no chat-wire equivalent.
				dropped = appendOnce(dropped, "tools."+t.Type)
				continue
			}
			fn := map[string]any{"name": t.Name}
			if t.Description != "" {
				fn["description"] = t.Description
			}
			if len(t.Parameters) > 0 {
				fn["parameters"] = json.RawMessage(t.Parameters)
			}
			tools = append(tools, map[string]any{"type": "function", "function": fn})
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}
	}
	if len(rr.ToolChoice) > 0 {
		var s string
		if err := json.Unmarshal(rr.ToolChoice, &s); err == nil {
			switch s {
			case "auto", "none", "required":
				body["tool_choice"] = s
			}
		} else {
			dropped = appendOnce(dropped, "tool_choice")
		}
	}
	if rr.MaxOutputTokens != nil {
		body["max_tokens"] = *rr.MaxOutputTokens
	}
	if rr.Temperature != nil {
		body["temperature"] = *rr.Temperature
	}
	if rr.TopP != nil {
		body["top_p"] = *rr.TopP
	}
	if rr.Stream {
		body["stream"] = true
		// Ask the chat pipeline for the final usage chunk so
		// response.completed can carry real token counts.
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	for name, raw := range map[string]json.RawMessage{
		"reasoning": rr.Reasoning, "include": rr.Include, "store": rr.Store,
		"prompt_cache_key": rr.PromptCacheKey, "parallel_tool_calls": rr.ParallelToolCalls,
		"client_metadata": rr.ClientMetadata,
	} {
		if len(raw) > 0 {
			dropped = appendOnce(dropped, name)
		}
	}
	b, err := json.Marshal(body)
	return b, dropped, err
}

func appendOnce(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// textOf flattens string-or-parts content to plain text.
func textOf(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("unsupported content shape")
	}
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text":
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "invalid_request_error"},
	})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(req.Body, maxBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read request body")
		return
	}
	var rr responsesRequest
	if err := json.Unmarshal(raw, &rr); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	chatBody, dropped, err := toChatRequest(&rr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unsupported Responses request: "+err.Error())
		return
	}
	if len(dropped) > 0 {
		// Disclosure before the pipeline runs: these are the adapter's own
		// drops, distinct from any provider-level strips the chat pipeline
		// will additionally disclose on the same header.
		w.Header().Set("x-inferplane-responses-params-dropped", strings.Join(dropped, ","))
	}

	inner := req.Clone(req.Context())
	inner.URL.Path = "/v1/chat/completions"
	inner.Body = io.NopCloser(bytes.NewReader(chatBody))
	inner.ContentLength = int64(len(chatBody))
	inner.Header = req.Header.Clone()
	inner.Header.Set("Content-Type", "application/json")
	inner.Header.Del("Content-Encoding")

	if rr.Stream {
		h.serveStream(w, inner)
		return
	}
	h.serveJSON(w, inner)
}

// --- non-streaming: buffer the chat JSON, translate once ---

type bufferingWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufferingWriter) Header() http.Header { return b.header }
func (b *bufferingWriter) WriteHeader(s int)   { b.status = s }
func (b *bufferingWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	return b.buf.Write(p)
}

type chatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Role      string          `json:"role"`
			Content   *string         `json:"content"`
			ToolCalls []chatToolCall  `json:"tool_calls"`
			Extra     json.RawMessage `json:"-"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

func respUsage(u *chatUsage) map[string]any {
	if u == nil {
		return nil
	}
	return map[string]any{
		"input_tokens": u.PromptTokens, "output_tokens": u.CompletionTokens,
		"total_tokens":          u.TotalTokens,
		"input_tokens_details":  map[string]any{"cached_tokens": 0},
		"output_tokens_details": map[string]any{"reasoning_tokens": 0},
	}
}

// outputItems converts one chat message (content + tool calls) into
// Responses output items. msgID, when non-empty, is reused for the message
// item so a stream's item.done matches its earlier item.added.
func outputItems(content string, calls []chatToolCall, msgID string) []map[string]any {
	var items []map[string]any
	if content != "" {
		if msgID == "" {
			msgID = "msg_" + ulid.New()
		}
		items = append(items, map[string]any{
			"type": "message", "id": msgID, "status": "completed",
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "annotations": []any{}, "text": content}},
		})
	}
	for _, c := range calls {
		items = append(items, map[string]any{
			"type": "function_call", "id": "fc_" + ulid.New(), "status": "completed",
			"call_id": c.ID, "name": c.Function.Name, "arguments": c.Function.Arguments,
		})
	}
	return items
}

func (h *Handler) serveJSON(w http.ResponseWriter, inner *http.Request) {
	bw := &bufferingWriter{header: w.Header()}
	h.chat.ServeHTTP(bw, inner)
	if bw.status != 200 {
		// Error bodies (auth, governance, routing) pass through untouched:
		// they are already the JSON error shape clients render.
		w.WriteHeader(bw.status)
		w.Write(bw.buf.Bytes())
		return
	}
	var cr chatResponse
	if err := json.Unmarshal(bw.buf.Bytes(), &cr); err != nil || len(cr.Choices) == 0 {
		writeErr(w, http.StatusBadGateway, "gateway could not translate the chat response")
		return
	}
	msg := cr.Choices[0].Message
	content := ""
	if msg.Content != nil {
		content = *msg.Content
	}
	resp := map[string]any{
		"id": "resp_" + ulid.New(), "object": "response", "status": "completed",
		"output": outputItems(content, msg.ToolCalls, ""),
	}
	if u := respUsage(cr.Usage); u != nil {
		resp["usage"] = u
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// --- streaming: translate chat SSE chunks into Responses SSE events ---

type streamShim struct {
	w       http.ResponseWriter
	fl      http.Flusher
	respID  string
	line    bytes.Buffer
	started bool
	// accumulation across chunks
	content  strings.Builder
	calls    []chatToolCall
	usage    *chatUsage
	seq      int
	upstatus int
	// msgItemID is minted when the first text delta arrives: the Responses
	// wire opens an item (response.output_item.added +
	// response.content_part.added) BEFORE any output_text.delta — a delta
	// without an open item is a client-side protocol error (Codex logs
	// "OutputTextDelta without active item").
	msgItemID string
}

func (s *streamShim) Header() http.Header { return s.w.Header() }

func (s *streamShim) WriteHeader(status int) {
	s.upstatus = status
	if status != 200 {
		s.w.WriteHeader(status)
		return
	}
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Del("Content-Length")
	s.w.WriteHeader(200)
	s.event("response.created", map[string]any{
		"response": map[string]any{"id": s.respID, "object": "response", "status": "in_progress"},
	})
	s.started = true
}

func (s *streamShim) Flush() {
	if s.fl != nil {
		s.fl.Flush()
	}
}

func (s *streamShim) event(typ string, fields map[string]any) {
	payload := map[string]any{"type": typ, "sequence_number": s.seq}
	s.seq++
	for k, v := range fields {
		payload[k] = v
	}
	b, _ := json.Marshal(payload)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", typ, b)
	s.Flush()
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content   *string `json:"content"`
			ToolCalls []struct {
				Index    int     `json:"index"`
				ID       *string `json:"id"`
				Function struct {
					Name      *string `json:"name"`
					Arguments *string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// Write receives the chat ingress's SSE bytes; frames are parsed line-wise.
func (s *streamShim) Write(p []byte) (int, error) {
	if s.upstatus != 0 && s.upstatus != 200 {
		return s.w.Write(p) // error body passthrough
	}
	if !s.started {
		s.WriteHeader(200)
	}
	s.line.Write(p)
	for {
		raw, err := bufio.NewReader(bytes.NewReader(s.line.Bytes())).ReadString('\n')
		if err != nil {
			break // incomplete line stays buffered
		}
		s.line.Next(len(raw))
		line := strings.TrimRight(raw, "\r\n")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue // we emit response.completed at Done() instead
		}
		var ch chatChunk
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		if ch.Usage != nil {
			s.usage = ch.Usage
		}
		for _, c := range ch.Choices {
			if c.Delta.Content != nil && *c.Delta.Content != "" {
				if s.msgItemID == "" {
					s.msgItemID = "msg_" + ulid.New()
					s.event("response.output_item.added", map[string]any{
						"output_index": 0,
						"item": map[string]any{
							"type": "message", "id": s.msgItemID, "status": "in_progress",
							"role": "assistant", "content": []any{},
						},
					})
					s.event("response.content_part.added", map[string]any{
						"item_id": s.msgItemID, "output_index": 0, "content_index": 0,
						"part": map[string]any{"type": "output_text", "annotations": []any{}, "text": ""},
					})
				}
				s.content.WriteString(*c.Delta.Content)
				s.event("response.output_text.delta", map[string]any{
					"item_id": s.msgItemID, "output_index": 0, "content_index": 0,
					"delta": *c.Delta.Content,
				})
			}
			for _, tc := range c.Delta.ToolCalls {
				for len(s.calls) <= tc.Index {
					s.calls = append(s.calls, chatToolCall{Type: "function"})
				}
				cur := &s.calls[tc.Index]
				if tc.ID != nil {
					cur.ID = *tc.ID
				}
				if tc.Function.Name != nil {
					cur.Function.Name += *tc.Function.Name
				}
				if tc.Function.Arguments != nil {
					cur.Function.Arguments += *tc.Function.Arguments
				}
			}
		}
	}
	return len(p), nil
}

// done closes the Responses stream: item completion events, then
// response.completed carrying the accumulated output and usage.
func (s *streamShim) done() {
	if !s.started {
		return // error path already passed through
	}
	items := outputItems(s.content.String(), s.calls, s.msgItemID)
	for _, it := range items {
		s.event("response.output_item.done", map[string]any{"item": it})
	}
	resp := map[string]any{
		"id": s.respID, "object": "response", "status": "completed", "output": items,
	}
	if u := respUsage(s.usage); u != nil {
		resp["usage"] = u
	}
	s.event("response.completed", map[string]any{"response": resp})
}

func (h *Handler) serveStream(w http.ResponseWriter, inner *http.Request) {
	fl, _ := w.(http.Flusher)
	shim := &streamShim{w: w, fl: fl, respID: "resp_" + ulid.New()}
	h.chat.ServeHTTP(shim, inner)
	shim.done()
}
