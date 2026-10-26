package bedrockapi

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

type nativeCapture struct {
	captureProvider
	events  []*providers.StreamEvent
	failure error
}

func (*nativeCapture) SupportsBedrockConverse(upstream string) bool { return upstream != "mantle-only" }
func (c *nativeCapture) Stream(_ context.Context, req *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	c.last = req
	return func(yield func(*providers.StreamEvent, error) bool) {
		for _, ev := range c.events {
			if !yield(ev, nil) {
				return
			}
		}
		if c.failure != nil {
			yield(nil, c.failure)
		}
	}, nil
}

func nativeIngress(t *testing.T, stream bool, gov *governance.Governor) (*InvokeHandler, *nativeCapture) {
	t.Helper()
	c := &nativeCapture{}
	h := holderFor(map[string]providers.Provider{"p": c}, map[string]config.ModelConfig{
		"native": {Targets: []config.Target{{Provider: "p", Model: "native-id"}}},
		"mantle": {Targets: []config.Target{{Provider: "p", Model: "mantle-only", API: "mantle"}}},
		"other":  {Targets: []config.Target{{Provider: "p", Model: "other-id"}}},
	})
	return NewConverseHandlerMetrics(router.New(h), h, nil, gov, nil, stream), c
}

func TestConverseIngressPreservesRequestAndUsesNativeProtocol(t *testing.T) {
	h, c := nativeIngress(t, false, nil)
	raw := `{ "messages":[{"role":"user","content":[{"text":"hello"},{"image":{"format":"png","source":{"bytes":"eA=="}}}]}], "inferenceConfig":{"maxTokens":16}, "additionalModelRequestFields":{"unknown":true} }`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(invokeReq("native", raw)))
	if rec.Code != 200 || c.last == nil || c.last.IngressProtocol != "bedrock-converse" || string(c.last.RawBody) != raw {
		t.Fatalf("native routing/passthrough failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestConverseIngressRejectsBeforeEgress(t *testing.T) {
	for _, tc := range []struct {
		name, model, body string
		auth, denied      bool
		want              int
	}{
		{"no principal", "native", `{}`, false, false, 401},
		{"model RBAC", "native", `{}`, true, true, 403},
		{"unknown model", "missing", `{}`, true, false, 404},
		{"mantle target", "mantle", `{}`, true, false, 404},
		{"invalid JSON", "native", `{"messages":`, true, false, 400},
		{"null body", "native", `null`, true, false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := nativeIngress(t, false, nil)
			req := invokeReq(tc.model, tc.body)
			if tc.auth {
				req = allowAll(req)
			}
			if tc.denied {
				req = req.WithContext(principal.With(req.Context(), keystore.Principal{AllowedModels: []string{"other"}}))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want || c.last != nil || !strings.Contains(rec.Body.String(), `"message"`) {
				t.Fatalf("got %d %s, called=%v", rec.Code, rec.Body.String(), c.last != nil)
			}
		})
	}
}

func TestConverseIngressEnforcesRateAdmission(t *testing.T) {
	gov := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
	h, c := nativeIngress(t, false, gov)
	for i, want := range []int{200, 429} {
		c.last = nil
		req := invokeReq("native", `{"messages":[]}`)
		p := keystore.Principal{KeyID: "fixture-key", AllowedModels: []string{"*"}}
		p.RPM = 1
		req = req.WithContext(principal.With(req.Context(), p))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want || (i == 1 && c.last != nil) {
			t.Fatalf("request %d: status %d, provider called=%v", i, rec.Code, c.last != nil)
		}
	}
}

func TestConversePrivacyAndStrictTargetCannotEscape(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		strict    bool
	}{
		{"detected PII", `{"messages":[{"role":"user","content":[{"text":"person@example.test"}]}]}`, false},
		{"opaque image", `{"messages":[{"role":"user","content":[{"image":{"format":"png","source":{"bytes":"eA=="}}}]}]}`, false},
		{"strict Mantle target", `{"messages":[{"role":"user","content":[{"text":"hello"}]}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := nativeIngress(t, false, nil)
			if tc.strict {
				h.r.SetBudgetConstraintGate(func(keystore.Principal) map[string]string { return map[string]string{"native": "mantle"} })
			} else {
				h.r.SetRoutingPolicyLookup(func(string, string) ([]*policy.Policy, error) {
					return []*policy.Policy{routingtest.Privacy(v1alpha1.Block)}, nil
				})
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, allowAll(invokeReq("native", tc.raw)))
			if rec.Code != 403 || c.last != nil {
				t.Fatalf("unsafe route: status=%d called=%v body=%s", rec.Code, c.last != nil, rec.Body.String())
			}
		})
	}
}

func TestConverseRegionLockRefusesUnlabelledTarget(t *testing.T) {
	h, c := nativeIngress(t, false, nil)
	h.SetTeamPolicy(func(string) (keystore.TeamRecord, bool) {
		return keystore.TeamRecord{AllowedRegions: []string{"approved-region"}}, true
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(invokeReq("native", `{"messages":[]}`)))
	if rec.Code != 403 || c.last != nil {
		t.Fatal("native ingress bypassed region lock")
	}
}

func TestConverseDoesNotRetryClientOrGuardrailErrors(t *testing.T) {
	h, _ := nativeIngress(t, false, nil)
	for _, status := range []int{400, 401, 403, 408, 424} {
		if h.retryError(&providers.UpstreamError{StatusCode: status}, false) {
			t.Fatalf("must not retry status %d", status)
		}
	}
	for _, status := range []int{429, 500, 503} {
		if !h.retryError(&providers.UpstreamError{StatusCode: status}, false) {
			t.Fatalf("expected bounded fallback for status %d", status)
		}
	}
}

func converseEvent(t *testing.T, kind string, chunk *schema.ChatChunk) *providers.StreamEvent {
	t.Helper()
	var raw bytes.Buffer
	if err := eventstream.NewEncoder().Encode(&raw, eventstream.Message{
		Headers: eventstream.Headers{
			{Name: ":message-type", Value: eventstream.StringValue("event")},
			{Name: ":event-type", Value: eventstream.StringValue(kind)},
		}, Payload: []byte(`{"native":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	return &providers.StreamEvent{Raw: raw.Bytes(), Chunk: chunk}
}

func TestConverseIngressStreamNeverWrapsNativeFrames(t *testing.T) {
	h, c := nativeIngress(t, true, nil)
	in, out := int64(10), int64(3)
	c.events = []*providers.StreamEvent{
		converseEvent(t, "futureEvent", nil),
		converseEvent(t, "contentBlockDelta", &schema.ChatChunk{Type: "content_block_delta"}),
		converseEvent(t, "messageStop", &schema.ChatChunk{Type: "message_stop"}),
		converseEvent(t, "metadata", &schema.ChatChunk{Usage: &schema.Usage{InputTokens: &in, OutputTokens: &out}}),
	}
	var want bytes.Buffer
	for _, ev := range c.events {
		want.Write(ev.Raw)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(invokeReq("native", `{"messages":[]}`)))
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), want.Bytes()) {
		t.Fatal("native frame bytes changed or trailing metadata lost")
	}
}

func TestConverseIngressIncompleteStreamEmitsException(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop bool
		err  error
	}{{"empty EOF", false, nil}, {"stop without metadata", true, nil}, {"broken transport", true, errors.New("interrupted")}} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := nativeIngress(t, true, nil)
			if tc.stop {
				c.events = []*providers.StreamEvent{converseEvent(t, "messageStop", &schema.ChatChunk{Type: "message_stop"})}
			}
			c.failure = tc.err
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, allowAll(invokeReq("native", `{"messages":[]}`)))
			decoder := eventstream.NewDecoder()
			reader := bytes.NewReader(rec.Body.Bytes())
			found := false
			for reader.Len() > 0 {
				frame, err := decoder.Decode(reader, nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, header := range frame.Headers {
					found = found || header.Name == ":exception-type"
				}
			}
			if !found {
				t.Fatal("truncated stream falsely reported normal completion")
			}
		})
	}
}

var _ http.Handler = (*InvokeHandler)(nil)
