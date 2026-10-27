package router

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/internal/sensitivity"
)

type modeRecordingInspector struct {
	modes        []sensitivity.InspectionMode
	legacyCalls  int
	omitEvidence bool
}

var evidenceClassifierCalls atomic.Int64

type evidenceCounterClassifier struct{ inspectionBenchmarkClassifier }

func (evidenceCounterClassifier) Score(ctx context.Context, windows []string) (classifier.Scores, error) {
	evidenceClassifierCalls.Add(1)
	return inspectionBenchmarkClassifier{}.Score(ctx, windows)
}

func init() {
	classifier.Register("inspection-evidence-counter", func(classifier.Config) (classifier.Classifier, error) {
		return evidenceCounterClassifier{}, nil
	})
}

func (i *modeRecordingInspector) Inspect(ctx context.Context, protocol string, raw []byte) (sensitivity.Result, error) {
	i.legacyCalls++
	return sensitivity.NewInspector().Inspect(ctx, protocol, raw)
}
func (i *modeRecordingInspector) InspectWithMode(ctx context.Context, protocol string, raw []byte, mode sensitivity.InspectionMode) (sensitivity.Result, error) {
	i.modes = append(i.modes, mode)
	result, err := sensitivity.NewInspector().(sensitivity.SelectiveInspector).InspectWithMode(ctx, protocol, raw, mode)
	if i.omitEvidence {
		result.SensitiveDataInspected = false
	}
	return result, err
}

func TestRoutingInspectionModes(t *testing.T) {
	for _, tt := range []struct {
		name, protocol           string
		privacy, strict, context bool
		want                     []sensitivity.InspectionMode
	}{
		{"no-policy-anthropic", "anthropic", false, false, false, nil},
		{"no-policy-openai", "openai", false, false, false, nil},
		{"no-policy-invoke", "bedrock", false, false, false, nil},
		{"no-policy-responses", "responses", false, false, false, []sensitivity.InspectionMode{sensitivity.StructureOnly}},
		{"context-shadow", "anthropic", false, false, true, []sensitivity.InspectionMode{sensitivity.StructureOnly}},
		{"strict", "anthropic", false, true, false, []sensitivity.InspectionMode{sensitivity.StructureOnly}},
		{"privacy", "anthropic", true, false, false, []sensitivity.InspectionMode{sensitivity.DetectSensitiveData}},
		{"privacy-responses", "responses", true, false, false, []sensitivity.InspectionMode{sensitivity.DetectSensitiveData}},
		{"privacy-shadow", "anthropic", true, false, true, []sensitivity.InspectionMode{sensitivity.DetectSensitiveData}},
		{"privacy-strict", "anthropic", true, true, false, []sensitivity.InspectionMode{sensitivity.DetectSensitiveData}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := routingConfig()
			if tt.protocol == "responses" || tt.protocol == "openai" {
				cfg = responsesContextConfig()
			}
			r, in := routingSetup(t, cfg)
			in.Protocol = tt.protocol
			if tt.protocol == "responses" {
				in.RawBody = []byte(`{"model":"premium","input":"synthetic","max_output_tokens":32}`)
				in.Compatible = func(ChainTarget) bool { return responses.ValidateConversion(in.RawBody) == nil }
			}
			if tt.privacy {
				installRoutingPolicies(r, privatePolicy("privacy", "private"))
			}
			if tt.context {
				docs := []*policy.Policy{contextPolicy("context", v1alpha1.Shadow, "economy")}
				if tt.privacy {
					docs = append(docs, privatePolicy("privacy", "private"))
				}
				installRoutingPolicies(r, docs...)
			}
			if tt.strict {
				r.SetBudgetConstraintGate(func(keystore.Principal) map[string]string { return map[string]string{"premium": "private"} })
			}
			recorder := &modeRecordingInspector{}
			r.SetRequestInspector(recorder)
			_, err := r.RouteRequest(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if recorder.legacyCalls != 0 || !reflect.DeepEqual(recorder.modes, tt.want) {
				t.Fatalf("inspection calls: legacy=%d, modes=%v; want %v", recorder.legacyCalls, recorder.modes, tt.want)
			}
		})
	}
}

func TestRoutingLegacyInspectorFallback(t *testing.T) {
	for _, mode := range []sensitivity.InspectionMode{sensitivity.StructureOnly, sensitivity.DetectSensitiveData} {
		calls := 0
		old := inspectFunc(func(ctx context.Context, protocol string, raw []byte) (sensitivity.Result, error) {
			calls++
			result, err := sensitivity.NewInspector().Inspect(ctx, protocol, raw)
			result.SensitiveDataInspected = false // Older implementations lack this field.
			return result, err
		})
		result, err := inspectForRoute(context.Background(), old, "anthropic", []byte(`{"messages":[{"role":"user","content":"person@example.test"}]}`), mode)
		if err != nil || calls != 1 || !result.SensitiveDataInspected || len(result.Categories) == 0 {
			t.Fatalf("legacy full fallback lost detection/evidence: %+v, calls=%d, %v", result, calls, err)
		}
	}
	r, in := routingSetup(t, routingConfig())
	protectedInput(&in)
	installRoutingPolicies(r, privatePolicy("privacy", "private"))
	r.SetRequestInspector(inspectFunc(func(ctx context.Context, protocol string, raw []byte) (sensitivity.Result, error) {
		result, err := sensitivity.NewInspector().Inspect(ctx, protocol, raw)
		result.SensitiveDataInspected = false
		return result, err
	}))
	got, err := r.RouteRequest(context.Background(), in)
	if err != nil || got.Model != "private" {
		t.Fatalf("legacy privacy lost enforcement: %+v, %v", got, err)
	}
}

func TestRoutingPrivacyRequiresInspectionEvidence(t *testing.T) {
	for _, protocol := range []string{"anthropic", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			cfg := responsesContextConfig()
			evidenceClassifierCalls.Store(0)
			cacheSize := 64
			cfg.PIIClassification = &config.PIIClassificationConfig{
				Cache:       &config.ClassifierCacheConfig{MaxEntries: &cacheSize},
				Classifiers: []config.ClassifierConfig{{Name: "clear", Type: "inspection-evidence-counter", Mode: "Enforce", Endpoint: "http://127.0.0.1:1", DataBoundary: "internal"}},
			}
			r, in := routingSetup(t, cfg)
			in.Protocol = protocol
			if protocol == "responses" {
				in.RawBody = []byte(`{"model":"premium","input":"synthetic"}`)
				in.Compatible = func(ChainTarget) bool { return responses.ValidateConversion(in.RawBody) == nil }
			}
			installRoutingPolicies(r, privatePolicy("privacy", "private"))
			recorder := &modeRecordingInspector{omitEvidence: true}
			r.SetRequestInspector(recorder)
			got, err := r.RouteRequest(context.Background(), in)
			requireDenied(t, got, err, "inspection_failed")
			if len(got.Decision.Classifiers) != 0 || evidenceClassifierCalls.Load() != 0 {
				t.Fatal("classifier ran or cached clear evidence after incomplete detection")
			}
			recorder.omitEvidence = false
			got, err = r.RouteRequest(context.Background(), in)
			if err != nil || evidenceClassifierCalls.Load() != 1 || len(got.Decision.Classifiers) != 1 || got.Decision.Classifiers[0].Result != "clear" {
				t.Fatalf("valid recovery reused unsupported clear evidence: calls=%d, %v", evidenceClassifierCalls.Load(), err)
			}
			if _, err := r.RouteRequest(context.Background(), in); err != nil || evidenceClassifierCalls.Load() != 1 {
				t.Fatal("successful complete inspection did not populate the clear cache")
			}
		})
	}
}

func TestRoutingShadowDoesNotLoosenPrivacy(t *testing.T) {
	r, in := routingSetup(t, routingConfig())
	protectedInput(&in)
	installRoutingPolicies(r, contextPolicy("shadow", v1alpha1.Shadow, "economy"), privatePolicy("privacy", "private"))
	recorder := &modeRecordingInspector{}
	r.SetRequestInspector(recorder)
	got, err := r.RouteRequest(context.Background(), in)
	if err != nil || got.Model != "private" || !reflect.DeepEqual(recorder.modes, []sensitivity.InspectionMode{sensitivity.DetectSensitiveData}) {
		t.Fatalf("shadow weakened privacy: %+v, modes=%v, %v", got, recorder.modes, err)
	}
}

func TestRoutingMaskReinspectionRequiresFullEvidence(t *testing.T) {
	for _, omitReinspection := range []bool{false, true} {
		r, in := routingSetup(t, routingConfig())
		protectedInput(&in)
		doc := privatePolicy("mask", "private")
		doc.Rules[0].SensitiveData.OnDetected = v1alpha1.Mask
		installRoutingPolicies(r, doc)
		r.SetRequestRedactor(sensitivity.NewRedactor())
		recorder := &modeRecordingInspector{}
		r.SetRequestInspector(&maskEvidenceInspector{recorder, omitReinspection})
		got, err := r.RouteRequest(context.Background(), in)
		if omitReinspection {
			requireDenied(t, got, err, "mask_failed")
		} else if err != nil || len(got.SanitizedBody) == 0 || !got.Decision.Masked {
			t.Fatalf("verified mask rejected: %+v, %v", got, err)
		}
		if !reflect.DeepEqual(recorder.modes, []sensitivity.InspectionMode{sensitivity.DetectSensitiveData, sensitivity.DetectSensitiveData}) {
			t.Fatalf("mask did not independently request full inspection: %v", recorder.modes)
		}
	}
}

type maskEvidenceInspector struct {
	*modeRecordingInspector
	omitSecond bool
}

func (i *maskEvidenceInspector) InspectWithMode(ctx context.Context, protocol string, raw []byte, mode sensitivity.InspectionMode) (sensitivity.Result, error) {
	result, err := i.modeRecordingInspector.InspectWithMode(ctx, protocol, raw, mode)
	if i.omitSecond && len(i.modes) == 2 {
		result.SensitiveDataInspected = false
	}
	return result, err
}
