package bedrock

// Guardrail-coverage fence (strategy P0 "Guardrail / residency", Phase 0a
// posture): for EVERY egress path in egressAPIs, a guarded request either
// carries the guardrail on the upstream call (invoke_model, converse) or is
// refused before egress (mantle — it has no guardrail parameter, and
// serving unguarded while the audit chain attests the guardrail is worse
// than refusing; the refusal is the documented permanent posture). An api
// value added to egressAPIs without a case here fails loudly, so a new
// egress cannot ship guard-unchecked.

import (
	"context"
	"testing"

	"github.com/inferplane/inferplane/providers"
)

func TestGuardrailFence_EveryEgressGuardedOrRefusing(t *testing.T) {
	guarded := &providers.ProxyRequest{
		Model: "m", Upstream: "up-model", RawBody: []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`),
		GuardrailID: "gr-team", GuardrailVersion: "2",
	}
	want := Guardrail{ID: "gr-team", Version: "2"}

	for _, api := range egressAPIs {
		t.Run(api, func(t *testing.T) {
			switch api {
			case "invoke_model":
				fi := &fakeInvoker{respBody: []byte(mockInvokeResp)}
				p := &provider{inv: fi, modelAPI: map[string]string{"up-model": api}}
				if _, err := p.Complete(context.Background(), guarded); err != nil {
					t.Fatal(err)
				}
				if fi.gotGuardrail != want {
					t.Fatalf("Complete: guardrail %+v never reached the upstream call, want %+v", fi.gotGuardrail, want)
				}
				fi.gotGuardrail = Guardrail{}
				fi.streamRaw = [][]byte{[]byte(`{"type":"message_stop"}`)}
				if _, err := p.Stream(context.Background(), guarded); err != nil {
					t.Fatal(err)
				}
				if fi.gotGuardrail != want {
					t.Fatalf("Stream: guardrail %+v never reached the upstream call, want %+v", fi.gotGuardrail, want)
				}
			case "converse":
				fc := &fakeConverser{resp: ConverseResponse{StopReason: "end_turn"}}
				p := &provider{conv: fc, modelAPI: map[string]string{"up-model": api}}
				if _, err := p.Complete(context.Background(), guarded); err != nil {
					t.Fatal(err)
				}
				if fc.gotReq.Guardrail != want {
					t.Fatalf("Complete: guardrail %+v never reached the upstream call, want %+v", fc.gotReq.Guardrail, want)
				}
				fc.gotReq = ConverseRequest{}
				if _, err := p.Stream(context.Background(), guarded); err != nil {
					t.Fatal(err)
				}
				if fc.gotReq.Guardrail != want {
					t.Fatalf("Stream: guardrail %+v never reached the upstream call, want %+v", fc.gotReq.Guardrail, want)
				}
			case "mantle":
				p := &provider{modelAPI: map[string]string{"up-model": api}}
				if _, err := p.Complete(context.Background(), guarded); err == nil {
					t.Fatal("Complete: a guarded request on the mantle path must be REFUSED (it has no guardrail parameter)")
				}
				if _, err := p.Stream(context.Background(), guarded); err == nil {
					t.Fatal("Stream: a guarded request on the mantle path must be REFUSED")
				}
			default:
				t.Fatalf("egress api %q has no guardrail-coverage case — extend this fence BEFORE shipping the new egress", api)
			}
		})
	}
}
