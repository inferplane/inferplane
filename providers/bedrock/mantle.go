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
// Guardrails do not apply on this path — Mantle has no guardrail parameter.
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
	"github.com/inferplane/inferplane/internal/openai"
	"github.com/inferplane/inferplane/pkg/schema"
	anthropicprov "github.com/inferplane/inferplane/providers/anthropic"

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
		client = &http.Client{}
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
// route gets "isn't supported on this route" (400).
func mantlePathFor(upstream string) string {
	switch {
	case strings.Contains(upstream, "anthropic."):
		return "/anthropic/v1/messages"
	case strings.Contains(upstream, "openai.") || strings.Contains(upstream, "xai."):
		return "/openai/v1/chat/completions"
	default:
		return "/v1/chat/completions"
	}
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
		return out, nil
	}
	if path == "/anthropic/v1/messages" {
		var parsed schema.ChatResponse
		if json.Unmarshal(raw, &parsed) == nil {
			out.Parsed = &parsed
		}
	} else if parsed, perr := openai.ResponseToCanonical(raw); perr == nil {
		// The Bedrock ingress tees RawBody to the client, so the chat
		// routes' OpenAI wire must be re-rendered in Anthropic shape under
		// the PUBLIC model name (same as completeConverse) — raw OpenAI
		// JSON must never reach an Anthropic-speaking client.
		parsed.Model = req.Model
		out.Parsed = parsed
		if rendered, rerr := json.Marshal(parsed); rerr == nil {
			out.RawBody = rendered
		}
	}
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
		inner = anthropicprov.ReadSSE(resp.Body)
	} else {
		inner = openai.ReadChatSSE(resp.Body)
	}
	return func(yield func(*providers.StreamEvent, error) bool) {
		defer resp.Body.Close()
		for ev, serr := range inner {
			if !yield(ev, serr) {
				return
			}
		}
	}, nil
}

var _ mantler = (*mantleClient)(nil)
