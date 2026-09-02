// Package providers defines the Provider interface and the transport types
// the gateway uses to proxy a request to an upstream LLM API. The canonical
// schema (pkg/schema) is kept pure; ProxyRequest/ProxyResponse/StreamEvent
// are transport wrappers that also carry the ORIGINAL upstream bytes, so the
// gateway can forward them verbatim and preserve the prompt-cache prefix
// (design doc §4.4) while still observing parsed content for governance.
package providers

import (
	"context"
	"iter"
	"net/http"
	"time"

	"github.com/inferplane/inferplane/pkg/schema"
)

// ProxyRequest is one inbound request resolved to a target. RawBody is what
// gets sent upstream UNMODIFIED — the gateway parses Parsed only to route and
// observe, never to re-serialize the request (cache invariant, §4.4).
type ProxyRequest struct {
	Model    string              // resolved model name (routing/observation)
	Parsed   *schema.ChatRequest // parsed for inspection; do NOT re-serialize for upstream
	RawBody  []byte              // original request bytes → forwarded verbatim
	Headers  http.Header         // anthropic-version / anthropic-beta passthrough
	Stream   bool                // req.stream
	Upstream string              // target model id at the upstream (may differ from Model)
	// IngressProtocol is the wire protocol the client spoke ("anthropic" |
	// "openai"). Providers compare it to their own native protocol: a match
	// forwards RawBody verbatim (lossless, cache-safe §3.3); a mismatch goes
	// through canonical conversion (best-effort).
	IngressProtocol string
	// GuardrailID/GuardrailVersion select a provider-level guardrail for THIS
	// request (per-team override, D6/ADR-019). Empty = the provider's
	// configured default. A deliberate, narrow exception to provider
	// isolation (§8): a transport field any provider may ignore — only
	// providers/bedrock reads it today.
	// ParamsStripped flows the OTHER way from every field above: the
	// PROVIDER appends the names of request parameters it dropped before
	// egress because the upstream model rejects them (bedrock's strip
	// tables), and the ingress reads it after Complete/Stream returns to
	// disclose the mutation (x-inferplane-params-stripped response header +
	// the audit record's params_stripped — strategy P1 "undisclosed
	// request mutation"). Providers that never mutate a request leave it
	// nil.
	ParamsStripped []string

	GuardrailID      string
	GuardrailVersion string
}

// ProxyResponse is a non-streaming upstream response. RawBody is teed to the
// client verbatim; Parsed is for observation (usage → audit/quota in M3/M5).
type ProxyResponse struct {
	StatusCode int
	Headers    http.Header
	RawBody    []byte
	Parsed     *schema.ChatResponse // nil if status != 2xx or body not parseable
}

// StreamEvent is one upstream SSE event. Raw is the exact event bytes
// (incl. "event:"/"data:" lines + blank-line terminator) teed to the client;
// Chunk is the parsed observation (nil for events with no JSON data payload,
// e.g. comment-only keepalives). This wrapper is why Stream yields
// *StreamEvent rather than *schema.ChatChunk: a single iter.Seq2 must carry
// BOTH the bytes to forward and the parsed view to observe.
type StreamEvent struct {
	Raw   []byte
	Chunk *schema.ChatChunk
}

// Provider proxies canonical requests to one upstream. New providers implement
// this in their own package; adding one touches providers/<name>/ + one line
// in registry.go and nothing in the core (design doc §8).
type Provider interface {
	Name() string
	Models() []schema.ModelInfo
	Complete(ctx context.Context, req *ProxyRequest) (*ProxyResponse, error)
	Stream(ctx context.Context, req *ProxyRequest) (iter.Seq2[*StreamEvent, error], error)
}

// TokenCounter is an optional capability. Providers that can count tokens
// upstream implement it; count_tokens falls back to an estimator otherwise
// (design doc §3.1, §10 #1).
type TokenCounter interface {
	CountTokens(ctx context.Context, req *ProxyRequest) (int64, error)
}

// HealthResult is the outcome of a provider connection probe (ADR-014 D2).
// Detail is a SANITIZED, human-readable status — it MUST never echo the
// api-key ref value or any secret. LatencyMS is the round-trip time of the
// probe call.
type HealthResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Detail    string `json:"detail"`
}

// HealthChecker is an optional capability (like TokenCounter): providers that
// can probe their upstream cheaply implement it so the admin console can test
// a provider's connectivity before trusting a route (ADR-014). A provider that
// does NOT implement it is reported as "probe unsupported" — never an error.
// The probe uses credentials the gateway already resolved server-side; the
// client never sends a secret.
type HealthChecker interface {
	HealthCheck(ctx context.Context) HealthResult
}

// CredentialSource supplies rotating upstream credentials to providers that
// opt in (auth.mode "broker", ADR-040). Provider-neutral on purpose: no AWS
// types cross the core/provider boundary, so a future GCP/Vertex provider can
// use the same seam. Implemented by internal/proxy.CredentialFetcher.
type CredentialSource interface {
	Credentials(ctx context.Context) (id, secret, session string, expires time.Time, err error)
}

// Config is the per-provider settings slice the registry hands to a factory.
// Kept minimal for M2; providers read what they need.
type Config struct {
	Type     string // "anthropic" | (M4) "bedrock" | (M5) "openai_compatible"
	BaseURL  string // upstream base, e.g. https://api.anthropic.com
	APIKey   string // resolved secret (never logged)
	Models   []schema.ModelInfo
	Settings map[string]string // provider-specific extras
	// HTTPClient, when non-nil, is used by HTTP-based providers (anthropic,
	// openai_compatible) instead of a default client. It lets the admin probe
	// inject a client with an SSRF-guarded DialContext (ADR-014 D2). nil ⇒
	// default client, so the data plane is unchanged. Ignored by bedrock (AWS SDK).
	HTTPClient *http.Client
	// Credentials, when non-nil, supplies rotating upstream credentials to a
	// provider whose auth mode opts in (ADR-040 decision 3). The gateway
	// injects it; nil ⇒ unchanged, and only providers/bedrock reads it today.
	// A bedrock provider with auth_mode "broker" and a nil Credentials is a
	// construction ERROR, never a fall-through to the node's local AWS
	// identity (ADR-040 fail-closed invariant #1).
	Credentials CredentialSource
}
