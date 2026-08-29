// Package openaicompat proxies to any OpenAI-compatible Chat Completions server
// (vLLM, Ollama, llm-d). Its native wire is the OpenAI protocol, so it applies
// the protocol-match forwarding rule (§3.3): when the ingress is also "openai"
// it forwards the client's RawBody VERBATIM (lossless, cache-safe) — only the
// top-level "model" field is rewritten to the upstream's model id; when the
// ingress is "anthropic" (or anything else) it converts the canonical request
// (req.Parsed) to OpenAI via internal/openai. Registered as "openai_compatible".
package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"

	"github.com/inferplane/inferplane/internal/openai"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

func init() { providers.Register("openai_compatible", factory) }

type provider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func factory(cfg providers.Config) (providers.Provider, error) {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &provider{baseURL: cfg.BaseURL, apiKey: cfg.APIKey, client: client}, nil
}

func (p *provider) Name() string { return "openai_compatible" }

func (p *provider) Models() []schema.ModelInfo { return nil } // models come from config

// buildBody produces the upstream OpenAI Chat Completions body. For an "openai"
// ingress the client's RawBody is forwarded byte-for-byte except the top-level
// "model" field, which is rewritten to req.Upstream via a top-level-map rewrite
// (like bedrock's toInvokeBody) so every other byte — and thus the prompt-cache
// prefix — is preserved. For any other ingress the canonical request is
// converted to OpenAI wire (best-effort) and the model set to req.Upstream.
func (p *provider) buildBody(req *providers.ProxyRequest, stream bool) ([]byte, error) {
	if req.IngressProtocol == "openai" {
		// Verbatim path: the client speaks OpenAI and already set stream /
		// stream_options as it wants them; only the model is rewritten.
		return rewriteModel(req.RawBody, req.Upstream)
	}
	converted := openai.CanonicalToRequest(req.Parsed)
	if stream {
		// A cross-protocol ingress selects streaming by OPERATION, not body
		// (Bedrock: /invoke-with-response-stream), so the flag must be
		// injected here — and include_usage with it, or vLLM emits no usage
		// frame and the stream settles with zero billable tokens.
		var top map[string]json.RawMessage
		if err := json.Unmarshal(converted, &top); err != nil {
			return nil, err
		}
		top["stream"] = json.RawMessage("true")
		top["stream_options"] = json.RawMessage(`{"include_usage":true}`)
		var err error
		if converted, err = json.Marshal(top); err != nil {
			return nil, err
		}
	}
	return rewriteModel(converted, req.Upstream)
}

// rewriteModel sets the top-level "model" field to model while preserving every
// other byte — including top-level key ORDER — so the request stays verbatim and
// the prompt-cache prefix is untouched (§4.4). It locates the top-level model
// VALUE span with a streaming token scan and splices the new value in place,
// rather than re-marshalling a map (which would reorder keys). If model is empty
// the body is returned unchanged; if there is no top-level "model" key the body
// is returned unchanged (the upstream uses its own default).
func rewriteModel(raw []byte, model string) ([]byte, error) {
	if model == "" {
		return raw, nil
	}
	start, end, ok, err := topLevelModelSpan(raw)
	if err != nil {
		return nil, err
	}
	if !ok {
		return raw, nil
	}
	repl, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(raw)-(end-start)+len(repl))
	out = append(out, raw[:start]...)
	out = append(out, repl...)
	out = append(out, raw[end:]...)
	return out, nil
}

// topLevelModelSpan returns the byte offsets [start,end) of the top-level
// "model" VALUE within raw (a JSON object), or ok=false if absent. It uses a
// json.Decoder at object depth 1 so nested "model" keys (e.g. inside a tool
// definition) are not matched. After a key it reads the full value — descending
// through any nested composites with depth tracking — so InputOffset() lands at
// the true value end. The returned start is trimmed of leading whitespace so the
// splice is byte-exact.
func topLevelModelSpan(raw []byte) (start, end int, ok bool, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return 0, 0, false, err
	}
	if d, isDelim := tok.(json.Delim); !isDelim || d != '{' {
		return 0, 0, false, nil // not a top-level object
	}
	for dec.More() {
		keyTok, kerr := dec.Token()
		if kerr != nil {
			return 0, 0, false, kerr
		}
		key, _ := keyTok.(string)
		valStart := int(dec.InputOffset())
		if verr := skipValue(dec); verr != nil {
			return 0, 0, false, verr
		}
		valEnd := int(dec.InputOffset())
		if key == "model" {
			// InputOffset after the key lands just past the closing quote, so
			// valStart includes the ":" separator and any surrounding spaces;
			// advance past both to point at the value's first byte.
			s := valStart
			for s < valEnd && (isJSONSpace(raw[s]) || raw[s] == ':') {
				s++
			}
			return s, valEnd, true, nil
		}
	}
	return 0, 0, false, nil
}

func isJSONSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// skipValue consumes exactly one JSON value from dec, fully descending through
// nested objects/arrays so the decoder is positioned right after the value.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if _, isDelim := tok.(json.Delim); !isDelim {
		return nil // scalar: a single token is the whole value
	}
	depth := 1 // entered an object or array
	for depth > 0 {
		t, terr := dec.Token()
		if terr != nil {
			return terr
		}
		if dd, ok := t.(json.Delim); ok {
			switch dd {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

func (p *provider) buildUpstream(ctx context.Context, req *providers.ProxyRequest, stream bool) (*http.Request, error) {
	body, err := p.buildBody(req, stream)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: build body: %w", err)
	}
	u, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	u.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		u.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	return u, nil
}

func (p *provider) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	u, err := p.buildUpstream(ctx, req, false)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(u)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: upstream call: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: read upstream: %w", err)
	}
	// RawBody is the provider's native OpenAI wire for an OpenAI ingress
	// (teed verbatim). Any other ingress (Anthropic re-renders from Parsed;
	// Bedrock tees RawBody) gets an Anthropic-shaped re-render under the
	// PUBLIC model name, so raw OpenAI JSON never reaches an
	// Anthropic-speaking client. Non-2xx still returns the upstream body
	// (teeable) with Parsed nil.
	out := &providers.ProxyResponse{StatusCode: resp.StatusCode, Headers: resp.Header, RawBody: body}
	if resp.StatusCode/100 == 2 {
		if parsed, perr := openai.ResponseToCanonical(body); perr == nil {
			out.Parsed = parsed
			if req.IngressProtocol != "openai" {
				parsed.Model = req.Model
				rendered, rerr := json.Marshal(parsed)
				if rerr != nil {
					return nil, fmt.Errorf("openaicompat: cannot render upstream response: %w", rerr)
				}
				out.RawBody = rendered
			}
		} else if req.IngressProtocol != "openai" {
			// An unparsed 2xx leaves Parsed nil, and the Bedrock ingress skips
			// settle entirely when it is: the request would bill nothing and
			// the client would get raw OpenAI JSON it cannot read.
			return nil, fmt.Errorf("openaicompat: re-render for %s ingress: %w", req.IngressProtocol, perr)
		}
	}
	return out, nil
}

func (p *provider) Stream(ctx context.Context, req *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	u, err := p.buildUpstream(ctx, req, true)
	if err != nil {
		return nil, err
	}
	u.Header.Set("Accept", "text/event-stream")
	resp, err := p.client.Do(u)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: upstream stream: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &providers.UpstreamError{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}
	}
	inner := openai.ReadChatSSE(resp.Body)
	return func(yield func(*providers.StreamEvent, error) bool) {
		defer resp.Body.Close()
		for ev, err := range inner {
			if !yield(ev, err) {
				return
			}
		}
	}, nil
}

var _ providers.Provider = (*provider)(nil)
