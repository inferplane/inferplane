package bedrock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"iter"
	"slices"

	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

// prepareResponses owns the cross-protocol envelope. RawBody is authoritative:
// Parsed may be an earlier observation, and must never be mutated or trusted
// to bypass admission. Other ingresses retain their original transport path.
func prepareResponses(req *providers.ProxyRequest) (*providers.ProxyRequest, responseToolNames, error) {
	if err := responses.ValidateConversion(req.RawBody); err != nil {
		return nil, nil, fmt.Errorf("bedrock responses: %w", err)
	}
	strict, err := responses.StrictTools(req.RawBody)
	if err != nil {
		return nil, nil, fmt.Errorf("bedrock responses: strict tools: %w", err)
	}
	if strict {
		return nil, nil, fmt.Errorf("bedrock responses: strict tool enforcement unavailable: %w", responses.ErrUnsupported)
	}
	canonical, err := responses.RequestToCanonical(req.RawBody)
	if err != nil {
		return nil, nil, fmt.Errorf("bedrock responses: parse: %w", err)
	}
	names, err := normalizeResponseTools(canonical)
	if err != nil {
		return nil, nil, fmt.Errorf("bedrock responses: tools: %w", err)
	}
	// Responses lists each parallel call/result separately. These are blocks
	// in one assistant/user turn on the Anthropic and Converse wires.
	var messages []schema.Message
	for _, msg := range canonical.Messages {
		if n := len(messages); n > 0 && messages[n-1].Role == msg.Role {
			messages[n-1].Content = append(messages[n-1].Content, msg.Content...)
		} else {
			messages = append(messages, msg)
		}
	}
	canonical.Messages = messages
	raw, err := responses.CanonicalToAnthropicRequest(canonical)
	if err != nil {
		return nil, nil, fmt.Errorf("bedrock responses: envelope: %w", err)
	}
	// Mantle's Chat route consumes Parsed, while Converse/InvokeModel and
	// Mantle's Anthropic route consume RawBody. Give both the same cleaned,
	// bounded envelope (including the renderer's default max_tokens).
	var cleaned schema.ChatRequest
	if err := json.Unmarshal(raw, &cleaned); err != nil {
		return nil, nil, fmt.Errorf("bedrock responses: canonical envelope: %w", err)
	}
	copy := *req
	copy.RawBody, copy.Parsed = raw, &cleaned
	copy.Headers = req.Headers.Clone()
	return &copy, names, nil
}

// responseToolNames maps offered backend names back to the original canonical
// names (which may themselves identify a Responses namespace). History-only
// names are normalized on input but never authorize a new model tool call.
type responseToolNames map[string]string

func normalizeResponseTools(cr *schema.ChatRequest) (responseToolNames, error) {
	var tools []map[string]json.RawMessage
	if len(cr.Tools) > 0 {
		if err := json.Unmarshal(cr.Tools, &tools); err != nil {
			return nil, responses.ErrInvalid
		}
	}
	forward, reverse := map[string]string{}, map[string]string{}
	add := func(name string) error {
		if name == "" {
			return responses.ErrInvalid
		}
		alias := name
		if len(name) > bedrockToolNameMax || !bedrockToolNameRE.MatchString(name) {
			// A domain-separated digest makes replay independent of tool order
			// or the other declarations in a request. The fixed ASCII prefix
			// also covers invalid first characters and multibyte input names.
			sum := sha256.Sum256([]byte("inferplane.bedrock.responses.tool.v1\x00" + name))
			alias = "iptool_" + hex.EncodeToString(sum[:24])
		}
		if prior, exists := reverse[alias]; exists && prior != name {
			return responses.ErrInvalid // includes collisions with literal user names
		}
		forward[name], reverse[alias] = alias, name
		return nil
	}
	offered := responseToolNames{}
	for _, tool := range tools {
		var name string
		if json.Unmarshal(tool["name"], &name) != nil {
			return nil, responses.ErrInvalid
		}
		if err := add(name); err != nil {
			return nil, err
		}
		offered[forward[name]] = name
		tool["name"], _ = json.Marshal(forward[name])
	}
	for i := range cr.Messages {
		for j := range cr.Messages[i].Content {
			block := &cr.Messages[i].Content[j]
			if block.Type == "tool_use" {
				if err := add(block.Name); err != nil {
					return nil, err
				}
				block.Name = forward[block.Name]
			}
		}
	}
	choice := parseToolChoice(cr.ToolChoice)
	switch choice.Type {
	case "tool":
		if _, ok := offered[forward[choice.Name]]; !ok {
			return nil, responses.ErrInvalid // never silently fall back to auto
		}
		cr.ToolChoice, _ = json.Marshal(map[string]string{"type": "tool", "name": forward[choice.Name]})
	case "any":
		if len(tools) == 0 {
			return nil, responses.ErrInvalid
		}
	case "none":
		// Converse has no forbid choice; no backend receives schemas that
		// could accidentally turn this into auto. History is still retained.
		cr.Tools, cr.ToolChoice = nil, nil
		return responseToolNames{}, nil
	}
	if len(tools) > 0 {
		cr.Tools, _ = json.Marshal(tools)
	}
	return offered, nil
}

func (names responseToolNames) restoreBlock(block *schema.ContentBlock) error {
	if block.Type != "tool_use" {
		return nil
	}
	name, ok := names[block.Name]
	if !ok {
		// Never echo a model-supplied unknown name or turn it into a seemingly
		// valid function call. The registry is request-local and immutable.
		return fmt.Errorf("bedrock responses: upstream returned an unknown tool")
	}
	block.Name = name
	return nil
}

func (names responseToolNames) restoreResponse(resp *schema.ChatResponse) (*schema.ChatResponse, error) {
	copy := *resp
	copy.Content = slices.Clone(resp.Content)
	for i := range copy.Content {
		if err := names.restoreBlock(&copy.Content[i]); err != nil {
			return nil, err
		}
	}
	return &copy, nil
}

func (names responseToolNames) complete(out *providers.ProxyResponse) (*providers.ProxyResponse, error) {
	if out == nil || out.StatusCode/100 != 2 || out.Parsed == nil {
		return out, nil
	}
	parsed, err := names.restoreResponse(out.Parsed)
	copy := *out
	if err != nil {
		// The call was served and has usage. Return a failed response with
		// accounting intact, not a transport error that discards that usage.
		copy.StatusCode = 502
		copy.RawBody = synthError(502, "bedrock responses: upstream returned an unknown tool").Body
		copy.Parsed = &schema.ChatResponse{Usage: out.Parsed.Usage}
		return &copy, nil
	}
	copy.Parsed = parsed
	copy.RawBody, err = json.Marshal(parsed)
	return &copy, err
}

func (names responseToolNames) stream(inner iter.Seq2[*providers.StreamEvent, error]) iter.Seq2[*providers.StreamEvent, error] {
	return func(yield func(*providers.StreamEvent, error) bool) {
		var failure error
		for ev, err := range inner {
			if err != nil {
				yield(nil, err)
				return
			}
			if ev == nil || ev.Chunk == nil {
				if failure == nil && !yield(ev, nil) {
					return
				}
				continue
			}
			chunk := *ev.Chunk
			if failure == nil && chunk.Message != nil {
				chunk.Message, failure = names.restoreResponse(chunk.Message)
			}
			if failure == nil && chunk.ContentBlock != nil {
				block := *chunk.ContentBlock
				failure = names.restoreBlock(&block)
				chunk.ContentBlock = &block
			}
			if failure != nil {
				// Converse reports usage after the last content block. Drain
				// only accounting under the caller's cancellable context, then
				// fail without emitting unknown tools or a successful stop.
				usage := ev.Chunk.Usage
				if ev.Chunk.Message != nil {
					usage = schema.MergeUsage(ev.Chunk.Message.Usage, usage)
				}
				if usage != nil && !yield(&providers.StreamEvent{Chunk: &schema.ChatChunk{Type: "message_delta", Usage: usage}}, nil) {
					return
				}
				continue
			}
			copy := *ev
			copy.Chunk = &chunk
			var raw bytes.Buffer
			if err := schema.WriteAnthropicSSE(&raw, &chunk); err != nil {
				yield(nil, err)
				return
			}
			copy.Raw = raw.Bytes()
			if !yield(&copy, nil) {
				return
			}
		}
		if failure != nil {
			yield(nil, failure)
		}
	}
}
