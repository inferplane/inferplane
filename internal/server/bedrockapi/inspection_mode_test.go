package bedrockapi

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/sensitivity"
	"github.com/inferplane/inferplane/internal/server/routingtest"
)

type nativeModeInspector struct {
	modes        []sensitivity.InspectionMode
	legacyCalls  int
	omitEvidence bool
}

func (i *nativeModeInspector) Inspect(ctx context.Context, protocol string, raw []byte) (sensitivity.Result, error) {
	i.legacyCalls++
	return sensitivity.NewInspector().Inspect(ctx, protocol, raw)
}
func (i *nativeModeInspector) InspectWithMode(ctx context.Context, protocol string, raw []byte, mode sensitivity.InspectionMode) (sensitivity.Result, error) {
	i.modes = append(i.modes, mode)
	result, err := sensitivity.NewInspector().(sensitivity.SelectiveInspector).InspectWithMode(ctx, protocol, raw, mode)
	if i.omitEvidence {
		result.SensitiveDataInspected = false
	}
	return result, err
}

func TestNativeConverseStructureOnlyPreflight(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		want      int
	}{
		{"unknown-opaque-bytes", `{ "messages":[{"role":"user","content":[{"text":"person@example.test"},{"image":{"format":"png","source":{"bytes":"YWJj"}}}]}], "additionalModelRequestFields":{"future":true} }`, 200},
		{"duplicate", `{"messages":[],"messages":[{"role":"user","content":[{"text":"synthetic"}]}]}`, 400},
		{"encoded-duplicate", `{"messages":[{"role":"user","content":[{"text":"{\"same\":1,\"same\":2}"}]}]}`, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, captured := nativeIngress(t, false, nil)
			recorder := &nativeModeInspector{}
			h.preflightInspector = recorder
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, allowAll(invokeReq("native", tt.raw)))
			if rec.Code != tt.want || !reflect.DeepEqual(recorder.modes, []sensitivity.InspectionMode{sensitivity.StructureOnly}) || recorder.legacyCalls != 0 {
				t.Fatalf("preflight: status=%d, modes=%v, legacy=%d", rec.Code, recorder.modes, recorder.legacyCalls)
			}
			if tt.want == 200 {
				if captured.last == nil || string(captured.last.RawBody) != tt.raw {
					t.Fatal("native unknown fields or bytes changed")
				}
			} else if captured.last != nil {
				t.Fatal("malformed structure dispatched")
			}
		})
	}
}

func TestNativeConversePrivacyStillFull(t *testing.T) {
	for _, tt := range []struct {
		name, raw    string
		omitEvidence bool
	}{
		{"pii", `{"messages":[{"role":"user","content":[{"text":"person@example.test"}]}]}`, false},
		{"opaque", `{"messages":[{"role":"user","content":[{"image":{"format":"png","source":{"bytes":"YWJj"}}}]}]}`, false},
		{"missing-evidence", `{"messages":[{"role":"user","content":[{"text":"synthetic"}]}]}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gov := governance.NewGovernor(nil, limiter.NewMemory(), budget.NewMemory(), nil)
			h, captured := nativeIngress(t, false, gov)
			recorder := &nativeModeInspector{omitEvidence: tt.omitEvidence}
			h.preflightInspector = recorder
			h.r.SetRequestInspector(recorder)
			h.r.SetRoutingPolicyLookup(func(string, string) ([]*policy.Policy, error) {
				return []*policy.Policy{routingtest.Privacy(v1alpha1.Block)}, nil
			})
			p := keystore.Principal{KeyID: "synthetic", AllowedModels: []string{"*"}}
			p.RPM = 1
			req := invokeReq("native", tt.raw)
			req = req.WithContext(principal.With(req.Context(), p))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 403 || captured.last != nil || !reflect.DeepEqual(recorder.modes, []sensitivity.InspectionMode{sensitivity.StructureOnly, sensitivity.DetectSensitiveData}) {
				t.Fatalf("privacy bypass: status=%d, dispatched=%v, modes=%v", rec.Code, captured.last != nil, recorder.modes)
			}
			// A privacy refusal must precede RPM/financial admission.
			h.r.SetRoutingPolicyLookup(nil)
			req = invokeReq("native", `{"messages":[{"role":"user","content":[{"text":"synthetic"}]}]}`)
			req = req.WithContext(principal.With(req.Context(), p))
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 200 || captured.last == nil {
				t.Fatalf("privacy refusal consumed admission: %d", rec.Code)
			}
		})
	}
}

func TestNativeConverseLegacyPreflightFallback(t *testing.T) {
	h, captured := nativeIngress(t, false, nil)
	calls := 0
	h.preflightInspector = nativeLegacyInspector(func(ctx context.Context, protocol string, raw []byte) (sensitivity.Result, error) {
		calls++
		return sensitivity.NewInspector().Inspect(ctx, protocol, raw)
	})
	raw := `{"messages":[{"role":"user","content":[{"text":"synthetic"}]}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(invokeReq("native", raw)))
	if rec.Code != 200 || captured.last == nil || calls != 1 {
		t.Fatalf("legacy preflight fallback: %d, calls=%d", rec.Code, calls)
	}
}

type nativeLegacyInspector func(context.Context, string, []byte) (sensitivity.Result, error)

func (f nativeLegacyInspector) Inspect(ctx context.Context, protocol string, raw []byte) (sensitivity.Result, error) {
	return f(ctx, protocol, raw)
}
