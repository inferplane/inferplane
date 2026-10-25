package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

// Native Converse is a separate wire contract from the existing Anthropic →
// Converse adapter. Raw request/response bytes and named eventstream frames
// must survive; canonical structs are observations for accounting only.
type nativeConverseClient struct{ transport *mantleClient }

func newNativeConverseClient(region string, creds aws.CredentialsProvider, client *http.Client, options ...bedrockruntime.Options) (*nativeConverseClient, error) {
	params := bedrockruntime.EndpointParameters{Region: aws.String(region)}
	if len(options) > 0 {
		o := options[0]
		if o.BaseEndpoint != nil && *o.BaseEndpoint != "" {
			// Preserve the established SDK transport for operators using an
			// endpoint override. Native wire transport is unavailable in that
			// configuration; never silently bypass their configured endpoint.
			return nil, nil
		}
		params.UseFIPS = aws.Bool(o.EndpointOptions.UseFIPSEndpoint == aws.FIPSEndpointStateEnabled)
		params.UseDualStack = aws.Bool(o.EndpointOptions.UseDualStackEndpoint == aws.DualStackEndpointStateEnabled)
	}
	endpoint, err := bedrockruntime.NewDefaultEndpointResolverV2().ResolveEndpoint(context.Background(),
		params)
	if err != nil {
		return nil, fmt.Errorf("bedrock converse endpoint: %w", err)
	}
	m := newMantleClient(endpoint.URI.String(), region, creds, client)
	// Copy the client: never mutate another provider's redirect policy. A
	// signed inference request must not follow a redirect or acquire retries.
	c := *m.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	m.client = &c
	return &nativeConverseClient{transport: m}, nil
}

func (n *nativeConverseClient) call(ctx context.Context, req *providers.ProxyRequest, stream bool) (*http.Response, error) {
	op := "converse"
	if stream {
		op = "converse-stream"
	}
	// The origin is SDK-resolved, and the entire configured model ID is one
	// escaped path segment. Neither client headers nor a client URL are used.
	resp, err := n.transport.do(ctx, "/model/"+url.PathEscape(req.Upstream)+"/"+op, req.RawBody, false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		// Do not echo AWS error messages (may contain account IDs, ARNs or
		// request content). Ingress renders the status in AWS error shape.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		status := resp.StatusCode
		if status < 400 {
			status = http.StatusBadGateway
		}
		return nil, synthError(status, "bedrock Converse upstream rejected the request")
	}
	if stream && resp.Header.Get("Content-Type") != "application/vnd.amazon.eventstream" {
		resp.Body.Close()
		return nil, synthError(502, "bedrock Converse returned an invalid stream content type")
	}
	return resp, nil
}

func (n *nativeConverseClient) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	resp, err := n.call(ctx, req, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	const limit = 64 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("bedrock Converse response: %w", err)
	}
	if len(raw) > limit || !json.Valid(raw) {
		return nil, synthError(502, "bedrock Converse returned an invalid response")
	}
	var observed struct {
		StopReason *string `json:"stopReason"`
	}
	_ = json.Unmarshal(raw, &observed)
	return &providers.ProxyResponse{
		StatusCode: resp.StatusCode, RawBody: raw,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Parsed:  &schema.ChatResponse{Usage: nativeConverseModelUsage(req.Upstream, raw), StopReason: observed.StopReason},
	}, nil
}

func (n *nativeConverseClient) Stream(ctx context.Context, req *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	resp, err := n.call(ctx, req, true)
	if err != nil {
		return nil, err
	}
	return func(yield func(*providers.StreamEvent, error) bool) {
		defer resp.Body.Close()
		decoder := eventstream.NewDecoder()
		for {
			var original bytes.Buffer
			frame, err := decoder.Decode(io.TeeReader(resp.Body, &original), nil)
			if errors.Is(err, io.EOF) && original.Len() == 0 {
				return
			}
			if err != nil {
				yield(nil, fmt.Errorf("bedrock Converse eventstream: %w", err))
				return
			}
			kind, messageType := "", ""
			for _, header := range frame.Headers {
				if header.Name == ":event-type" {
					kind = header.Value.String()
				}
				if header.Name == ":message-type" {
					messageType = header.Value.String()
				}
			}
			if messageType == "exception" || messageType == "error" {
				yield(nil, errors.New("bedrock Converse stream exception"))
				return
			}
			if messageType != "event" {
				yield(nil, errors.New("bedrock Converse invalid event type"))
				return
			}
			chunk := &schema.ChatChunk{}
			switch kind {
			case "messageStop":
				var stop struct {
					Reason string `json:"stopReason"`
				}
				if json.Unmarshal(frame.Payload, &stop) != nil || stop.Reason == "" {
					yield(nil, errors.New("bedrock Converse invalid messageStop"))
					return
				}
				chunk.Type = "message_stop"
			case "metadata":
				chunk.Usage = nativeConverseModelUsage(req.Upstream, frame.Payload)
				if chunk.Usage.InputTokens == nil || chunk.Usage.OutputTokens == nil {
					yield(nil, errors.New("bedrock Converse metadata missing valid token counts"))
					return
				}
			case "contentBlockDelta":
				var delta struct {
					Delta struct {
						Text *string `json:"text"`
					} `json:"delta"`
				}
				if json.Unmarshal(frame.Payload, &delta) == nil && delta.Delta.Text != nil {
					chunk.Type = "content_block_delta"
				}
			}
			if !yield(&providers.StreamEvent{Raw: original.Bytes(), Chunk: chunk}, nil) {
				return
			}
		}
	}, nil
}

func nativeConverseModelUsage(upstream string, raw []byte) *schema.Usage {
	u := nativeConverseUsage(raw)
	// Cached OpenAI Converse counts have differed across model generations.
	// Keep AWS's observed counts intact, but do not assert that multiplying
	// these fields proves the billable total without a qualified model rule.
	if strings.Contains(upstream, "openai.") &&
		((u.CacheReadInputTokens != nil && *u.CacheReadInputTokens > 0) ||
			(u.CacheCreationInputTokens != nil && *u.CacheCreationInputTokens > 0)) {
		u.AccountingUncertain = true
	}
	return u
}

// Converse inputTokens, cacheReadInputTokens and cacheWriteInputTokens are
// separate reported fields. Do not apply the translated adapter's historical
// model-name heuristics to the native contract. Missing/invalid counts and
// writes without explicit TTL details retain accounting uncertainty.
func nativeConverseUsage(raw []byte) *schema.Usage {
	var body struct {
		Usage *struct {
			Input      *int64 `json:"inputTokens"`
			Output     *int64 `json:"outputTokens"`
			CacheRead  *int64 `json:"cacheReadInputTokens"`
			CacheWrite *int64 `json:"cacheWriteInputTokens"`
			Details    []struct {
				Input *int64 `json:"inputTokens"`
				TTL   string `json:"ttl"`
			} `json:"cacheDetails"`
		} `json:"usage"`
	}
	u := &schema.Usage{AccountingUncertain: true}
	if json.Unmarshal(raw, &body) != nil || body.Usage == nil {
		return u
	}
	v := body.Usage
	u.InputTokens, u.OutputTokens = v.Input, v.Output
	u.CacheReadInputTokens, u.CacheCreationInputTokens = v.CacheRead, v.CacheWrite
	valid := true
	for _, field := range []**int64{&u.InputTokens, &u.OutputTokens, &u.CacheReadInputTokens, &u.CacheCreationInputTokens} {
		if *field != nil && **field < 0 {
			*field = nil
			valid = false
		}
	}
	ttlKnown := u.CacheCreationInputTokens == nil || *u.CacheCreationInputTokens == 0
	if len(v.Details) > 0 {
		var five, hour, total int64
		seen := map[string]bool{}
		detailsValid := true
		for _, detail := range v.Details {
			if detail.Input == nil || *detail.Input < 0 || *detail.Input > math.MaxInt64-total ||
				(detail.TTL != "5m" && detail.TTL != "1h") || seen[detail.TTL] {
				detailsValid = false
				break
			}
			seen[detail.TTL] = true
			total += *detail.Input
			if detail.TTL == "5m" {
				five = *detail.Input
			} else {
				hour = *detail.Input
			}
		}
		ttlKnown = detailsValid && u.CacheCreationInputTokens != nil && total == *u.CacheCreationInputTokens
		if ttlKnown {
			u.CacheCreation = &schema.CacheCreation{Ephemeral5mInputTokens: &five, Ephemeral1hInputTokens: &hour}
		}
	}
	u.AccountingUncertain = !valid || u.InputTokens == nil || u.OutputTokens == nil || !ttlKnown
	return u
}

func (p *provider) SupportsBedrockConverse(upstream string) bool {
	return p.native != nil && p.apiFor(upstream) != "mantle"
}

func (p *provider) prepareNativeConverse(req *providers.ProxyRequest) (*providers.ProxyRequest, error) {
	if !p.SupportsBedrockConverse(req.Upstream) {
		return nil, synthError(400, "native Converse requires a Bedrock Runtime target")
	}
	body, ok := uniqueMembers(req.RawBody)
	if !ok {
		return nil, synthError(400, "invalid Converse request object")
	}
	// Bedrock treats a case variant as a different member; a case-folding
	// parser would not. Refuse rather than guess which one applies.
	for name := range body {
		for _, guarded := range []string{"guardrailConfig", "serviceTier", "performanceConfig"} {
			if name != guarded && strings.EqualFold(name, guarded) {
				return nil, synthError(400, "ambiguous Converse policy field")
			}
		}
	}
	// The current price table is keyed by provider/model, not service tier.
	// An unpriced priority/optimized request could invalidate hard money
	// bounds. Refuse it explicitly instead of silently changing its bytes.
	for _, modifier := range []struct{ field, key, supported string }{
		{"serviceTier", "type", "default"}, {"performanceConfig", "latency", "standard"},
	} {
		if raw, exists := body[modifier.field]; exists {
			value, ok := exactStringMembers(raw, modifier.key)
			if !ok || value[modifier.key] != modifier.supported {
				return nil, synthError(400, "native Converse requires the default service tier and standard latency pricing")
			}
		}
	}
	g := p.guardrailFor(req)
	if g.ID == "" {
		return req, nil
	}
	if g.Version == "" {
		g.Version = "DRAFT"
	}
	if value, exists := body["guardrailConfig"]; exists {
		// Only the exact required pair may be restated. Extra members such
		// as streamProcessingMode or trace would change how the mandatory
		// guardrail runs, so they conflict too.
		supplied, ok := exactStringMembers(value, "guardrailIdentifier", "guardrailVersion")
		if !ok || supplied["guardrailIdentifier"] != g.ID || supplied["guardrailVersion"] != g.Version {
			return nil, synthError(400, "Converse guardrail conflicts with the required guardrail")
		}
		return req, nil
	}
	guardrail, _ := json.Marshal(map[string]string{"guardrailIdentifier": g.ID, "guardrailVersion": g.Version})
	// Append only the policy-required top-level field. Existing message,
	// cache, tool and unknown fields retain their original bytes and order.
	end := bytes.LastIndexByte(req.RawBody, '}')
	raw := append([]byte(nil), req.RawBody[:end]...)
	if len(body) > 0 {
		raw = append(raw, ',')
	}
	raw = append(raw, `"guardrailConfig":`...)
	raw = append(raw, guardrail...)
	raw = append(raw, req.RawBody[end:]...)
	copy := *req
	copy.RawBody = raw
	return &copy, nil
}

// uniqueMembers decodes one JSON object level, refusing duplicate names and
// trailing data. Policy fields are checked by exact name because Bedrock
// matches member names case-sensitively, whereas encoding/json keeps the
// last duplicate and its struct decoding folds case.
func uniqueMembers(raw []byte) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	members := map[string]json.RawMessage{}
	for d.More() {
		t, err := d.Token()
		name, ok := t.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, duplicate := members[name]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		members[name] = value
	}
	if t, err := d.Token(); err != nil || t != json.Delim('}') {
		return nil, false
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, false
	}
	return members, true
}

// exactStringMembers requires an object with exactly the named members, each
// a JSON string.
func exactStringMembers(raw json.RawMessage, names ...string) (map[string]string, bool) {
	members, ok := uniqueMembers(raw)
	if !ok || len(members) != len(names) {
		return nil, false
	}
	values := make(map[string]string, len(names))
	for _, name := range names {
		member := bytes.TrimSpace(members[name])
		var value string
		if len(member) == 0 || member[0] != '"' || json.Unmarshal(member, &value) != nil {
			return nil, false
		}
		values[name] = value
	}
	return values, true
}
