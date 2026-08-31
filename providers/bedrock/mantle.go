// Bedrock Mantle egress — the real implementation of the `"mantle"` api kind
// ADR-022 deferred. Mantle (`https://bedrock-mantle.<region>.api.aws`, SigV4
// service "bedrock") serves Bedrock models through provider-native API shapes
// instead of InvokeModel/Converse, with strictly vendor-partitioned routes
// (each 400s on the others' models — probed live 2026-08-28):
//
//	anthropic.*        → POST /anthropic/v1/messages        (Anthropic Messages API)
//	openai.* / xai.*   → POST /openai/v1/chat/completions   (OpenAI Chat Completions)
//	every other vendor → POST /v1/chat/completions          (OpenAI Chat Completions)
//
// Some models (openai.gpt-5.4/-5.5) exist ONLY here, so the invoke_model
// fallback sent them to an endpoint that has never heard of them. Bedrock
// Guardrails do not apply on this path — Mantle has no guardrail parameter — so
// a request carrying an effective guardrail is refused before it gets here
// (bedrock.go's mantleGuardrailCheck), never served unguarded.
package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	anthropicsse "github.com/inferplane/inferplane/internal/anthropic"
	"github.com/inferplane/inferplane/internal/openai"
	"github.com/inferplane/inferplane/pkg/schema"

	"github.com/inferplane/inferplane/providers"
)

// mantler is the seam Complete/Stream dispatch through, so tests can fake the
// mantle path the same way invoker/converser are faked.
type mantler interface {
	Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error)
	Stream(ctx context.Context, req *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error)
}

type mantleClient struct {
	baseURL string
	region  string
	creds   aws.CredentialsProvider
	signer  *v4.Signer
	client  *http.Client
}

func newMantleClient(baseURL, region string, creds aws.CredentialsProvider, client *http.Client) *mantleClient {
	if client == nil {
		// A zero-value client has no timeout at all, so a hung Mantle connection
		// would pin the request goroutine indefinitely. The bound goes on the
		// transport, not Client.Timeout: the latter also caps body reading, which
		// would truncate any SSE stream that outlives it.
		tr, ok := http.DefaultTransport.(*http.Transport)
		if ok {
			tr = tr.Clone()
		} else {
			tr = &http.Transport{}
		}
		tr.ResponseHeaderTimeout = 120 * time.Second
		client = &http.Client{Transport: tr}
	}
	return &mantleClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		region:  region,
		creds:   creds,
		signer:  v4.NewSigner(),
		client:  client,
	}
}

// mantlePathFor picks the vendor route for an upstream model id. The
// partitioning is exact, not a preference — a model sent to another vendor's
// route gets "isn't supported on this route" (400). The vendor is matched as
// a whole dot-separated segment (not a substring), so a geo prefix still
// routes ("us.anthropic.…") while an id that merely CONTAINS a vendor name
// ("notanthropic.…") cannot be captured by another vendor's route.
func mantlePathFor(upstream string) string {
	for _, seg := range strings.Split(upstream, ".") {
		switch seg {
		case "anthropic":
			return "/anthropic/v1/messages"
		case "openai", "xai":
			return "/openai/v1/chat/completions"
		}
	}
	return "/v1/chat/completions"
}

// toMantleAnthropicBody rewrites a Bedrock-ingress Anthropic body for Mantle's
// native Anthropic route: drop "anthropic_version" (an InvokeModel-only body
// field — the real Anthropic contract takes the version as a header), set the
// model (Bedrock ingress carries it in the URL, Mantle wants it in the body)
// and the stream flag (Bedrock selects streaming by OPERATION, Mantle by body
// field). Top-level-only rewrite, same as toInvokeBody: system/messages/tools
// values stay byte-identical so the prompt-cache prefix is preserved (§4.4).
func toMantleAnthropicBody(raw []byte, upstream string, stream bool) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	delete(top, "anthropic_version")
	model, err := json.Marshal(upstream)
	if err != nil {
		return nil, err
	}
	top["model"] = model
	if stream {
		top["stream"] = json.RawMessage("true")
	} else {
		delete(top, "stream")
	}
	return json.Marshal(top)
}

// mantleChatStripParams lists OpenAI-wire params a Mantle chat-completions
// model rejects with a 400 — the same evidence-based style as
// converseUnsupportedInference, in this wire's field names. gpt-5.6 rejects
// temperature (any value but its default)/top_p/stop; gpt-5.4/-5.5,
// xai.grok-4.3, and the bare-route vendors (deepseek/zai/moonshotai) accepted
// all of them when probed (2026-08-28).
var mantleChatStripParams = []struct {
	match  string
	params []string
}{
	{"openai.gpt-5.6", []string{"temperature", "top_p", "stop"}},
}

// toMantleChatBody renders the canonical request onto Mantle's OpenAI
// chat-completions wire: internal/openai's conversion plus three Mantle
// specifics — the model is the upstream id, max_tokens is renamed
// max_completion_tokens (the gpt-5.6 family rejects max_tokens outright and
// every probed Mantle model accepts the newer name), and streaming asks for
// include_usage so the final chunk carries billable token counts.
func toMantleChatBody(req *providers.ProxyRequest, upstream string, stream bool) ([]byte, error) {
	if req.Parsed == nil {
		return nil, fmt.Errorf("bedrock mantle: request for %s has no parsed body", upstream)
	}
	cr := *req.Parsed
	cr.Model = upstream
	cr.Stream = nil // re-added below from the operation, not the body
	var top map[string]json.RawMessage
	if err := json.Unmarshal(openai.CanonicalToRequest(&cr), &top); err != nil {
		return nil, err
	}
	if mt, has := top["max_tokens"]; has {
		top["max_completion_tokens"] = mt
		delete(top, "max_tokens")
	}
	if stream {
		top["stream"] = json.RawMessage("true")
		top["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	} else {
		delete(top, "stream")
	}
	for _, e := range mantleChatStripParams {
		if !strings.Contains(upstream, e.match) {
			continue
		}
		for _, p := range e.params {
			delete(top, p)
		}
	}
	return json.Marshal(top)
}

// do builds, signs (SigV4, service "bedrock"), and sends one Mantle request.
func (m *mantleClient) do(ctx context.Context, path string, body []byte, sse bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.HasSuffix(path, "/messages") {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if sse {
		req.Header.Set("Accept", "text/event-stream")
	}
	creds, err := m.creds.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("bedrock mantle: credentials: %w", err)
	}
	sum := sha256.Sum256(body)
	if err := m.signer.SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), "bedrock", m.region, time.Now()); err != nil {
		return nil, fmt.Errorf("bedrock mantle: sign: %w", err)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bedrock mantle: upstream call: %w", err)
	}
	return resp, nil
}

func (m *mantleClient) buildBody(req *providers.ProxyRequest, path string, stream bool) ([]byte, error) {
	if path == "/anthropic/v1/messages" {
		return toMantleAnthropicBody(req.RawBody, req.Upstream, stream)
	}
	return toMantleChatBody(req, req.Upstream, stream)
}

func (m *mantleClient) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	path := mantlePathFor(req.Upstream)
	body, err := m.buildBody(req, path, false)
	if err != nil {
		return nil, err
	}
	resp, err := m.do(ctx, path, body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("bedrock mantle: read upstream: %w", err)
	}
	out := &providers.ProxyResponse{StatusCode: resp.StatusCode, Headers: resp.Header, RawBody: raw}
	if resp.StatusCode/100 != 2 {
		// The Anthropic ingress tees non-2xx RawBody verbatim, and the chat
		// routes speak OpenAI — re-render their {"error":{...}} envelope in
		// Anthropic shape, same invariant as the success path. The anthropic
		// route's errors are already Anthropic-shaped and pass through.
		if path != "/anthropic/v1/messages" {
			out.RawBody = anthropicErrorBody(resp.StatusCode, raw)
		}
		return out, nil
	}
	// A 2xx body we cannot parse must NOT be forwarded: out.Parsed stays nil,
	// and the ingress skips settle/metering entirely when it is — the request
	// would bill nothing and audit identically to a genuinely free model
	// (ADR-030's zero-cost bug class). Fail the call instead.
	var parsed *schema.ChatResponse
	if path == "/anthropic/v1/messages" {
		var pv schema.ChatResponse
		if err := json.Unmarshal(raw, &pv); err != nil {
			return nil, synthError(502, "bedrock mantle: unparseable upstream response body")
		}
		parsed = &pv
	} else if pv, perr := openai.ResponseToCanonical(raw); perr != nil {
		return nil, synthError(502, "bedrock mantle: unparseable upstream response body")
	} else {
		parsed = pv
	}
	// Mantle answers under the UPSTREAM model id, but the client asked for the
	// public name (same rewrite completeConverse does). Re-rendering is lossless:
	// ChatResponse round-trips unknown fields through Extra. The Bedrock ingress
	// tees RawBody to the client, so the chat routes' OpenAI wire has to be
	// re-rendered in Anthropic shape — raw OpenAI JSON must never reach an
	// Anthropic-speaking client.
	parsed.Model = req.Model
	out.Parsed = parsed
	rendered, rerr := json.Marshal(parsed)
	if rerr != nil {
		return nil, synthError(502, "bedrock mantle: cannot render upstream response")
	}
	out.RawBody = rendered
	return out, nil
}

func (m *mantleClient) Stream(ctx context.Context, req *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	path := mantlePathFor(req.Upstream)
	body, err := m.buildBody(req, path, true)
	if err != nil {
		return nil, err
	}
	resp, err := m.do(ctx, path, body, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &providers.UpstreamError{StatusCode: resp.StatusCode, Body: raw, Header: resp.Header}
	}
	var inner iter.Seq2[*providers.StreamEvent, error]
	if path == "/anthropic/v1/messages" {
		inner = anthropicsse.ReadSSE(resp.Body)
	} else {
		inner = openai.ReadChatSSE(resp.Body)
	}
	return func(yield func(*providers.StreamEvent, error) bool) {
		defer resp.Body.Close()
		// Fail-closed parity with Complete and openaicompat.Stream: the
		// Bedrock ingress re-renders from ev.Chunk, so a 200 stream whose
		// every frame fails to parse would otherwise end cleanly with zero
		// canonical frames — and settle zero billable tokens for a served
		// request (ADR-030's zero-cost class).
		var sawChunk bool
		for ev, serr := range inner {
			if ev != nil && ev.Chunk != nil {
				sawChunk = true
				// Echo the PUBLIC model name, matching Complete: streamed
				// message_start frames otherwise leak the internal upstream
				// id ("anthropic.claude-opus-5") — and the Raw bytes the
				// Anthropic ingress tees verbatim must be regenerated to
				// match, or only the re-rendering ingresses get the rewrite.
				if ev.Chunk.Message != nil && ev.Chunk.Message.Model != req.Model {
					ev.Chunk.Message.Model = req.Model
					if len(ev.Raw) > 0 {
						var buf bytes.Buffer
						if schema.WriteAnthropicSSE(&buf, ev.Chunk) == nil {
							ev.Raw = buf.Bytes()
						}
					}
				}
			}
			if !yield(ev, serr) {
				return
			}
		}
		if !sawChunk {
			yield(nil, fmt.Errorf("bedrock mantle: upstream stream for %s produced no parseable frames", req.Upstream))
		}
	}, nil
}

var _ mantler = (*mantleClient)(nil)

// anthropicErrorBody re-renders an OpenAI {"error":{...}} envelope in
// Anthropic shape, preserving the upstream message. Unparseable input gets a
// fixed message rather than being echoed (it may not be JSON at all).
func anthropicErrorBody(status int, raw []byte) []byte {
	msg := "bedrock mantle: upstream error"
	var oai struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &oai) == nil && oai.Error.Message != "" {
		msg = oai.Error.Message
	}
	body, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": anthropicErrType(status), "message": msg},
	})
	return body
}
