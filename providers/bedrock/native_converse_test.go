package bedrock

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/inferplane/inferplane/providers"
)

func TestNativeConverseEndpointHonorsFIPSAndDualStack(t *testing.T) {
	options := bedrockruntime.Options{}
	options.EndpointOptions.UseFIPSEndpoint = aws.FIPSEndpointStateEnabled
	options.EndpointOptions.UseDualStackEndpoint = aws.DualStackEndpointStateEnabled
	n, err := newNativeConverseClient("us-west-2", nativeCredentials{}, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if n.transport.baseURL != "https://bedrock-runtime-fips.us-west-2.api.aws" {
		t.Fatalf("configured FIPS/dualstack lost: %s", n.transport.baseURL)
	}
	options.BaseEndpoint = aws.String("https://operator-endpoint.invalid")
	n, err = newNativeConverseClient("us-west-2", nativeCredentials{}, nil, options)
	if err != nil || n != nil {
		t.Fatal("native transport must not bypass an operator endpoint override")
	}
}

func TestNativeConverseCachedOpenAICountsRemainUncertainAndUnmodified(t *testing.T) {
	u := nativeConverseModelUsage("openai.gpt-fixture", []byte(`{"usage":{"inputTokens":100,"outputTokens":5,"cacheReadInputTokens":80}}`))
	if !u.AccountingUncertain || *u.InputTokens != 100 || *u.CacheReadInputTokens != 80 {
		t.Fatal("unqualified cached input semantics were treated as billable certainty or rewritten")
	}
}

func TestNativeConverseRejectsCorruptFramesAndMissingMetadataUsage(t *testing.T) {
	corrupt := nativeFrame(t, "metadata", `{"usage":{"inputTokens":1,"outputTokens":1}}`)
	corrupt[len(corrupt)-1] ^= 1
	for _, wire := range [][]byte{
		corrupt,
		nativeFrame(t, "metadata", `{}`),
		nativeFrame(t, "metadata", `{"usage":{"inputTokens":-1,"outputTokens":1}}`),
	} {
		n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			_, _ = w.Write(wire)
		})
		events, err := n.Stream(context.Background(), &providers.ProxyRequest{Upstream: "fixture", RawBody: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		failed := false
		for ev, err := range events {
			failed = failed || err != nil
			if ev != nil {
				t.Fatal("invalid stream frame forwarded as a successful event")
			}
		}
		if !failed {
			t.Fatal("invalid stream ended successfully")
		}
	}
}

type nativeCredentials struct{}

func (nativeCredentials) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "fixture-access", SecretAccessKey: "fixture-secret"}, nil
}

func nativeFixture(t *testing.T, fn http.HandlerFunc) *nativeConverseClient {
	t.Helper()
	s := httptest.NewServer(fn)
	t.Cleanup(s.Close)
	return &nativeConverseClient{transport: newMantleClient(s.URL, "us-east-1", nativeCredentials{}, s.Client())}
}

func TestNativeConversePreservesWireAndSignsWithOwnCredentials(t *testing.T) {
	body := []byte(`{ "messages":[{"role":"user","content":[{"text":"hello"},{"futureBlock":{"opaque":"keep"}}]}], "inferenceConfig":{"maxTokens":1}, "additionalModelRequestFields":{"future":true} }`)
	answer := []byte(`{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":11,"outputTokens":2,"cacheReadInputTokens":7,"cacheWriteInputTokens":0},"futureResponse":true}`)
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, body) || r.URL.Path != "/model/profile/model/converse" {
			t.Errorf("request changed: path=%s body=%s", r.URL.Path, got)
		}
		if r.URL.EscapedPath() != "/model/profile%2Fmodel/converse" {
			t.Error("configured model ID escaped out of its path segment")
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=fixture-access/") ||
			r.Header.Get("X-Api-Key") != "" {
			t.Error("must use gateway SigV4 credentials only")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(answer)
	})
	out, err := n.Complete(context.Background(), &providers.ProxyRequest{
		Upstream: "profile/model", RawBody: body, Headers: http.Header{"Authorization": {"Bearer client-secret"}, "X-Api-Key": {"client-secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.RawBody, answer) || out.Parsed.Usage.AccountingUncertain ||
		*out.Parsed.Usage.InputTokens != 11 || *out.Parsed.Usage.CacheReadInputTokens != 7 {
		t.Fatalf("wire or disjoint usage changed: %+v", out)
	}
}

func nativeFrame(t *testing.T, kind, payload string) []byte {
	t.Helper()
	var b bytes.Buffer
	err := eventstream.NewEncoder().Encode(&b, eventstream.Message{
		Headers: eventstream.Headers{
			{Name: ":message-type", Value: eventstream.StringValue("event")},
			{Name: ":event-type", Value: eventstream.StringValue(kind)},
			{Name: ":content-type", Value: eventstream.StringValue("application/json")},
		}, Payload: []byte(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestNativeConverseStreamPreservesNamedFramesAndMetadata(t *testing.T) {
	wire := bytes.Join([][]byte{
		nativeFrame(t, "messageStart", `{"role":"assistant"}`),
		nativeFrame(t, "futureEvent", `{"opaque":"kept"}`),
		nativeFrame(t, "contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"hello"}}`),
		nativeFrame(t, "messageStop", `{"stopReason":"end_turn"}`),
		nativeFrame(t, "metadata", `{"usage":{"inputTokens":10,"outputTokens":1}}`),
	}, nil)
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/converse-stream") || r.Header.Get("Accept") != "application/vnd.amazon.eventstream" {
			t.Error("wrong stream operation or content negotiation")
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(wire)
	})
	seq, err := n.Stream(context.Background(), &providers.ProxyRequest{Upstream: "model", RawBody: []byte(`{"messages":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	var stopped, usage bool
	for ev, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		got.Write(ev.Raw)
		if ev.Chunk != nil {
			stopped = stopped || ev.Chunk.Type == "message_stop"
			usage = usage || ev.Chunk.Usage != nil
		}
	}
	if !bytes.Equal(got.Bytes(), wire) || !stopped || !usage {
		t.Fatal("native stream bytes, stop, or trailing usage lost")
	}
}

func TestNativeConverseUsageNeverInventsCacheTTL(t *testing.T) {
	for _, body := range []string{
		`{"usage":{"inputTokens":10,"outputTokens":2,"cacheWriteInputTokens":20}}`,
		`{"usage":{"inputTokens":10}}`,
		`{"usage":{"inputTokens":-1,"outputTokens":2}}`,
		`{"usage":{"inputTokens":"10","outputTokens":2}}`,
		`{}`,
	} {
		u := nativeConverseUsage([]byte(body))
		if u == nil || !u.AccountingUncertain {
			t.Fatalf("unknown accounting treated as complete: %s", body)
		}
	}
}

func TestNativeConverseUsageCacheTTLDetails(t *testing.T) {
	u := nativeConverseUsage([]byte(`{"usage":{"inputTokens":10,"outputTokens":2,"cacheWriteInputTokens":30,"cacheDetails":[{"inputTokens":20,"ttl":"1h"},{"inputTokens":10,"ttl":"5m"}]}}`))
	if u.AccountingUncertain || u.CacheCreation == nil ||
		*u.CacheCreation.Ephemeral5mInputTokens != 10 || *u.CacheCreation.Ephemeral1hInputTokens != 20 {
		t.Fatal("explicit cache TTL information lost")
	}
	for _, details := range []string{
		`[{"inputTokens":30,"ttl":"24h"}]`,
		`[{"inputTokens":20,"ttl":"1h"}]`,
		`[{"inputTokens":15,"ttl":"5m"},{"inputTokens":15,"ttl":"5m"}]`,
	} {
		u := nativeConverseUsage([]byte(`{"usage":{"inputTokens":10,"outputTokens":2,"cacheWriteInputTokens":30,"cacheDetails":` + details + `}}`))
		if !u.AccountingUncertain {
			t.Fatalf("invalid cache details accepted: %s", details)
		}
	}
}

func TestNativeConverseRejectsGuardrailOverrideBeforeDispatch(t *testing.T) {
	calls := 0
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	p := &provider{native: n, defaultGuardrail: Guardrail{ID: "required", Version: "1"}}
	_, err := p.Complete(context.Background(), &providers.ProxyRequest{IngressProtocol: "bedrock-converse", Upstream: "model",
		RawBody: []byte(`{"messages":[],"guardrailConfig":{"guardrailIdentifier":"other","guardrailVersion":"1"}}`)})
	if err == nil || calls != 0 {
		t.Fatal("client guardrail must not override mandatory guardrail")
	}
}

func TestNativeConverseNoRedirectOrHiddenRetry(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls, leaked := 0, 0
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked++ }))
			defer destination.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", destination.URL)
				w.WriteHeader(status)
			}))
			defer source.Close()
			n, err := newNativeConverseClient("us-east-1", nativeCredentials{}, source.Client())
			if err != nil {
				t.Fatal(err)
			}
			n.transport.baseURL = source.URL // private test seam, no production endpoint override
			_, err = n.Complete(context.Background(), &providers.ProxyRequest{Upstream: "model", RawBody: []byte(`{"messages":[]}`)})
			if err == nil || calls != 1 || leaked != 0 {
				t.Fatalf("hidden request/redirect: error=%v calls=%d leaked=%d", err != nil, calls, leaked)
			}
		})
	}
}

func TestNativeConverseRequiredGuardrailInsertionKeepsClientBody(t *testing.T) {
	raw := []byte(`{ "messages":[{"role":"user","content":[{"text":"keep whitespace"}]}], "future":{"x":1} }`)
	var sent []byte
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		io.WriteString(w, `{"usage":{"inputTokens":1,"outputTokens":1}}`)
	})
	p := &provider{native: n, defaultGuardrail: Guardrail{ID: "required", Version: "1"}}
	req := &providers.ProxyRequest{IngressProtocol: "bedrock-converse", Upstream: "model", RawBody: raw}
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(req.RawBody, raw) || !bytes.HasPrefix(sent, raw[:len(raw)-1]) ||
		!bytes.Contains(sent, []byte(`"guardrailIdentifier":"required"`)) {
		t.Fatal("mandatory guardrail insertion changed existing request bytes")
	}
}

func TestNativeConverseRefusesUnpricedTierModifiers(t *testing.T) {
	calls := 0
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	p := &provider{native: n}
	for _, body := range []string{
		`{"messages":[],"serviceTier":{"type":"priority"}}`,
		`{"messages":[],"serviceTier":{"type":"flex"}}`,
		`{"messages":[],"performanceConfig":{"latency":"optimized"}}`,
	} {
		_, err := p.Complete(context.Background(), &providers.ProxyRequest{
			IngressProtocol: "bedrock-converse", Upstream: "model", RawBody: []byte(body),
		})
		if err == nil {
			t.Fatal("unpriced tier reached provider")
		}
	}
	if calls != 0 {
		t.Fatal("tier validation ran after egress")
	}
}

// Bedrock parses member names case-sensitively, while encoding/json struct
// decoding folds case and keeps the last match. Ambiguous or extended policy
// fields must therefore be refused instead of validated through a struct.
func TestNativeConverseRefusesAmbiguousGuardrailConfig(t *testing.T) {
	calls := 0
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"usage":{"inputTokens":1,"outputTokens":1}}`)
	})
	p := &provider{native: n, defaultGuardrail: Guardrail{ID: "required", Version: "1"}}
	for _, body := range []string{
		`{"messages":[],"guardrailConfig":{"guardrailIdentifier":"other","GUARDRAILIDENTIFIER":"required","guardrailVersion":"1"}}`,
		`{"messages":[],"guardrailConfig":{"GuardrailIdentifier":"required","guardrailVersion":"1"}}`,
		`{"messages":[],"guardrailConfig":{"guardrailIdentifier":"required","guardrailVersion":"1","streamProcessingMode":"async"}}`,
		`{"messages":[],"GuardrailConfig":{"guardrailIdentifier":"other","guardrailVersion":"1"}}`,
	} {
		if _, err := p.Complete(context.Background(), &providers.ProxyRequest{
			IngressProtocol: "bedrock-converse", Upstream: "model", RawBody: []byte(body),
		}); err == nil {
			t.Errorf("ambiguous guardrail accepted: %s", body)
		}
	}
	if calls != 0 {
		t.Fatalf("ambiguous guardrail reached egress %d times", calls)
	}
	exact := []byte(`{"messages":[],"guardrailConfig":{"guardrailIdentifier":"required","guardrailVersion":"1"}}`)
	req := &providers.ProxyRequest{IngressProtocol: "bedrock-converse", Upstream: "model", RawBody: exact}
	if _, err := p.Complete(context.Background(), req); err != nil || calls != 1 || !bytes.Equal(req.RawBody, exact) {
		t.Fatalf("exact required guardrail refused or rewritten: err=%v calls=%d", err, calls)
	}
}

func TestNativeConverseRefusesCaseVariantTierModifiers(t *testing.T) {
	calls := 0
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"usage":{"inputTokens":1,"outputTokens":1}}`)
	})
	p := &provider{native: n}
	for _, body := range []string{
		`{"messages":[],"ServiceTier":{"type":"priority"}}`,
		`{"messages":[],"serviceTier":{"type":"default","Type":"priority"}}`,
		`{"messages":[],"PerformanceConfig":{"latency":"optimized"}}`,
		`{"messages":[],"performanceConfig":{"latency":"standard","Latency":"optimized"}}`,
	} {
		if _, err := p.Complete(context.Background(), &providers.ProxyRequest{
			IngressProtocol: "bedrock-converse", Upstream: "model", RawBody: []byte(body),
		}); err == nil {
			t.Errorf("ambiguous tier modifier accepted: %s", body)
		}
	}
	if calls != 0 {
		t.Fatalf("ambiguous tier modifier reached egress %d times", calls)
	}
	if _, err := p.Complete(context.Background(), &providers.ProxyRequest{
		IngressProtocol: "bedrock-converse", Upstream: "model",
		RawBody: []byte(`{"messages":[],"serviceTier":{"type":"default"},"performanceConfig":{"latency":"standard"}}`),
	}); err != nil || calls != 1 {
		t.Fatalf("default tier and standard latency refused: err=%v calls=%d", err, calls)
	}
}
