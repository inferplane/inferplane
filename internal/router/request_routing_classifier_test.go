package router

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/live"
)

var routerFakes sync.Map // classifier name → *routerFake, read at State build time

type routerFake struct {
	mu     sync.Mutex
	inputs [][]string
	decide func([]string) (classifier.Verdict, error)
}

func (f *routerFake) Windows(s []string) []string { return s }

func (f *routerFake) Score(_ context.Context, windows []string) (classifier.Scores, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, slices.Clone(windows))
	f.mu.Unlock()
	v, err := f.decide(windows)
	if err != nil {
		return classifier.Scores{}, err
	}
	verdicts := make([]classifier.Verdict, len(windows))
	for i := range verdicts {
		verdicts[i] = v
	}
	return classifier.Scores{Verdicts: verdicts, Sent: len(windows)}, nil
}

func (f *routerFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

func init() {
	classifier.Register("router-test", func(c classifier.Config) (classifier.Classifier, error) {
		f, ok := routerFakes.Load(c.Name)
		if !ok {
			return nil, fmt.Errorf("no fake %q", c.Name)
		}
		return f.(*routerFake), nil
	})
}

func always(v classifier.Verdict) func([]string) (classifier.Verdict, error) {
	return func([]string) (classifier.Verdict, error) { return v, nil }
}

func classifierRouting(mode string, teams ...string) *config.Config {
	cfg := routingConfig()
	cfg.PIIClassification = &config.PIIClassificationConfig{Classifiers: []config.ClassifierConfig{{
		Name: "fake", Type: "router-test", Mode: mode, Teams: teams, Endpoint: "http://127.0.0.1:1", DataBoundary: "internal",
	}}}
	return cfg
}

func classifierSetup(t *testing.T, mode string, decide func([]string) (classifier.Verdict, error), teams ...string) (*Router, RequestRoutingInput, *routerFake) {
	t.Helper()
	f := &routerFake{decide: decide}
	routerFakes.Store("fake", f)
	r, in := routingSetup(t, classifierRouting(mode, teams...))
	return r, in, f
}

type fakeEntry struct {
	name, mode string
	decide     func([]string) (classifier.Verdict, error)
}

// multiClassifierSetup configures one fake classifier per entry, in order.
func multiClassifierSetup(t *testing.T, entries ...fakeEntry) (*Router, RequestRoutingInput, []*routerFake) {
	t.Helper()
	cfg := routingConfig()
	cfg.PIIClassification = &config.PIIClassificationConfig{}
	fakes := make([]*routerFake, len(entries))
	for i, e := range entries {
		fakes[i] = &routerFake{decide: e.decide}
		routerFakes.Store(e.name, fakes[i])
		cfg.PIIClassification.Classifiers = append(cfg.PIIClassification.Classifiers, config.ClassifierConfig{
			Name: e.name, Type: "router-test", Mode: e.mode, Endpoint: "http://127.0.0.1:1", DataBoundary: "internal",
		})
	}
	r, in := routingSetup(t, cfg)
	return r, in, fakes
}

// Ruling 23: a flag from one Enforce entry and an error from another apply
// both obligations, so the stricter one wins, as for the regex triggers.
func TestClassifierFlagDoesNotHideAnotherEntrysFailure(t *testing.T) {
	fail := func([]string) (classifier.Verdict, error) { return 0, classifier.ErrTransport }
	for name, second := range map[string]func([]string) (classifier.Verdict, error){"error": fail, "abstain": always(classifier.Abstain)} {
		t.Run(name, func(t *testing.T) {
			r, in, fakes := multiClassifierSetup(t, fakeEntry{"fake-a", "Enforce", always(classifier.Flagged)}, fakeEntry{"fake-b", "Enforce", second})
			p := privatePolicy("privacy", "private-alias") // onDetected InternalOnly
			p.Rules[0].SensitiveData.OnUninspectable = v1alpha1.Block
			installRoutingPolicies(r, p)
			in.AllowedRegions = []string{"eu"}
			got, err := r.RouteRequest(context.Background(), in)
			requireDenied(t, got, err, "sensitive_blocked")
			if fakes[0].calls() != 1 || fakes[1].calls() != 1 || !slices.Contains(got.Decision.Categories, "model_pii") {
				t.Fatalf("calls %d/%d, evidence %+v", fakes[0].calls(), fakes[1].calls(), got.Decision)
			}
		})
	}
	t.Run("both InternalOnly", func(t *testing.T) {
		r, in, _ := multiClassifierSetup(t, fakeEntry{"fake-a", "Enforce", always(classifier.Flagged)}, fakeEntry{"fake-b", "Enforce", fail})
		installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
		in.AllowedRegions = []string{"eu"}
		got, err := r.RouteRequest(context.Background(), in)
		if err != nil || got.Model != "private" || got.Decision.Privacy != "internal_only" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

// Only Enforce entries restrict, whatever the other entries say.
func TestClassifierMixedShadowAndEnforce(t *testing.T) {
	r0, in0 := routingSetup(t, routingConfig())
	installRoutingPolicies(r0, privatePolicy("privacy", "private-alias"))
	unrestricted, err := r0.RouteRequest(context.Background(), in0)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("Shadow flag, Enforce clear", func(t *testing.T) {
		r, in, _ := multiClassifierSetup(t, fakeEntry{"shadow", "Shadow", always(classifier.Flagged)}, fakeEntry{"enforce", "Enforce", always(classifier.Clear)})
		installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
		got, err := r.RouteRequest(context.Background(), in)
		if err != nil || got.Model != unrestricted.Model || len(got.Chain) != len(unrestricted.Chain) || got.Decision.Privacy != unrestricted.Decision.Privacy || slices.Contains(got.Decision.Categories, "model_pii") {
			t.Fatalf("a Shadow flag restricted routing: %+v %v", got, err)
		}
		want := []ClassifierRef{
			{Name: "shadow", Boundary: "internal", Mode: "Shadow", Result: "flagged", WouldRestrict: true},
			{Name: "enforce", Boundary: "internal", Mode: "Enforce", Result: "clear"},
		}
		if !reflect.DeepEqual(got.Decision.Classifiers, want) {
			t.Fatalf("evidence %+v", got.Decision.Classifiers)
		}
	})
	t.Run("Enforce flag, Shadow clear", func(t *testing.T) {
		r, in, _ := multiClassifierSetup(t, fakeEntry{"shadow", "Shadow", always(classifier.Clear)}, fakeEntry{"enforce", "Enforce", always(classifier.Flagged)})
		installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
		in.AllowedRegions = []string{"eu"}
		got, err := r.RouteRequest(context.Background(), in)
		if err != nil || got.Model != "private" || got.Decision.Privacy != "internal_only" || !slices.Contains(got.Decision.Categories, "model_pii") {
			t.Fatalf("an Enforce flag did not restrict: %+v %v", got, err)
		}
		for _, ct := range got.Chain {
			if ct.DataBoundary != "internal" {
				t.Fatalf("external target in chain: %+v", ct)
			}
		}
		want := []ClassifierRef{
			{Name: "shadow", Boundary: "internal", Mode: "Shadow", Result: "clear"},
			{Name: "enforce", Boundary: "internal", Mode: "Enforce", Result: "flagged"},
		}
		if !reflect.DeepEqual(got.Decision.Classifiers, want) {
			t.Fatalf("evidence %+v", got.Decision.Classifiers)
		}
	})
}

func TestClassifierFlaggedRestrictsToInternalModels(t *testing.T) {
	r, in, _ := classifierSetup(t, "Enforce", always(classifier.Flagged))
	installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
	in.AllowedRegions = []string{"eu"}
	got, err := r.RouteRequest(context.Background(), in)
	if err != nil || got.Model != "private" || got.Decision.Privacy != "internal_only" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, ct := range got.Chain {
		if ct.DataBoundary != "internal" {
			t.Fatalf("external target in chain: %+v", ct)
		}
	}
	want := []ClassifierRef{{Name: "fake", Boundary: "internal", Mode: "Enforce", Result: "flagged"}}
	if !slices.Contains(got.Decision.Categories, "model_pii") || !reflect.DeepEqual(got.Decision.Classifiers, want) {
		t.Fatalf("evidence %v %+v", got.Decision.Categories, got.Decision.Classifiers)
	}
}

func TestClassifierShadowNeverChangesRouting(t *testing.T) {
	r0, in0 := routingSetup(t, routingConfig())
	installRoutingPolicies(r0, privatePolicy("privacy", "private-alias"))
	want, err := r0.RouteRequest(context.Background(), in0)
	if err != nil {
		t.Fatal(err)
	}
	r, in, f := classifierSetup(t, "Shadow", always(classifier.Flagged))
	installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
	got, err := r.RouteRequest(context.Background(), in)
	if err != nil || got.Model != want.Model || len(got.Chain) != len(want.Chain) || got.Decision.Reason != want.Decision.Reason || f.calls() != 1 {
		t.Fatalf("Shadow changed routing: %+v %v", got, err)
	}
	if slices.Contains(got.Decision.Categories, "model_pii") || len(got.Decision.Classifiers) != 1 || !got.Decision.Classifiers[0].WouldRestrict {
		t.Fatalf("Shadow evidence %v %+v", got.Decision.Categories, got.Decision.Classifiers)
	}
}

// Review Focus 3: an unavailable or uncertain classifier fails closed in Enforce.
func TestClassifierErrorAppliesOnUninspectable(t *testing.T) {
	fail := func([]string) (classifier.Verdict, error) { return 0, classifier.ErrTransport }
	for name, decide := range map[string]func([]string) (classifier.Verdict, error){"error": fail, "abstain": always(classifier.Abstain)} {
		t.Run(name+"/block", func(t *testing.T) {
			r, in, _ := classifierSetup(t, "Enforce", decide)
			p := privatePolicy("privacy", "private-alias")
			p.Rules[0].SensitiveData.OnUninspectable = v1alpha1.Block
			installRoutingPolicies(r, p)
			got, err := r.RouteRequest(context.Background(), in)
			requireDenied(t, got, err, "sensitive_blocked")
			if got.Decision.Classifiers[0].Result != name {
				t.Fatalf("result %+v", got.Decision.Classifiers)
			}
		})
		t.Run(name+"/internal", func(t *testing.T) {
			r, in, _ := classifierSetup(t, "Enforce", decide)
			installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
			in.AllowedRegions = []string{"eu"}
			got, err := r.RouteRequest(context.Background(), in)
			if err != nil || got.Model != "private" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestClassifierFlaggedUnderMaskRuleUsesOnUninspectable(t *testing.T) {
	r, in, _ := classifierSetup(t, "Enforce", always(classifier.Flagged))
	installRoutingPolicies(r, maskPolicy()) // onDetected Mask, onUninspectable Block
	calls := 0
	setAdaptiveRedactor(t, r, func(_ context.Context, _ string, raw []byte) ([]byte, error) { calls++; return raw, nil })
	got, err := r.RouteRequest(context.Background(), in)
	requireDenied(t, got, err, "sensitive_blocked")
	if calls != 0 {
		t.Fatal("a Block obligation must win before masking")
	}

	r2, in2, _ := classifierSetup(t, "Enforce", always(classifier.Flagged))
	p := privatePolicy("mask", "private")
	p.Rules[0].SensitiveData.OnDetected = v1alpha1.Mask // onUninspectable stays InternalOnly
	installRoutingPolicies(r2, p)
	protectedInput(&in2)
	in2.AllowedRegions = []string{"eu"}
	setAdaptiveRedactor(t, r2, func(_ context.Context, _ string, raw []byte) ([]byte, error) {
		return bytes.ReplaceAll(raw, []byte("person@example.test"), []byte("REDACTED")), nil
	})
	got2, err := r2.RouteRequest(context.Background(), in2)
	if err != nil || got2.Model != "private" || !got2.Decision.Masked || bytes.Contains(got2.SanitizedBody, []byte("person@example.test")) {
		t.Fatalf("regex Mask plus classifier flag: %+v %v", got2, err)
	}
	if !slices.Contains(got2.Decision.Categories, "model_pii") || !slices.Contains(got2.Decision.Categories, "email") {
		t.Fatalf("masked evidence lost a category: %v", got2.Decision.Categories)
	}
}

// Policy compilation only admits InternalOnly or Block for onUninspectable
// (policy.go), but the router must not depend on that: a classifier flag under
// a rule whose two actions are both Mask has nothing to redact and blocks.
func TestClassifierFlaggedUnderDoubleMaskRuleBlocks(t *testing.T) {
	r, in, _ := classifierSetup(t, "Enforce", always(classifier.Flagged))
	p := maskPolicy()
	p.Rules[0].SensitiveData.OnUninspectable = v1alpha1.Mask
	installRoutingPolicies(r, p)
	setAdaptiveRedactor(t, r, func(_ context.Context, _ string, raw []byte) ([]byte, error) { return raw, nil })
	got, err := r.RouteRequest(context.Background(), in)
	requireDenied(t, got, err, "sensitive_blocked")
}

// Review Focus 5: no call, no latency outside the classifier's scope.
func TestClassifierNotCalledOutsideScope(t *testing.T) {
	t.Run("other team", func(t *testing.T) {
		r, in, f := classifierSetup(t, "Enforce", always(classifier.Flagged), "other")
		installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
		got, err := r.RouteRequest(context.Background(), in)
		if err != nil || f.calls() != 0 || got.Decision.Classifiers != nil {
			t.Fatalf("%+v %v calls=%d", got, err, f.calls())
		}
	})
	t.Run("no privacy rule", func(t *testing.T) {
		r, in, f := classifierSetup(t, "Enforce", always(classifier.Flagged))
		if _, err := r.RouteRequest(context.Background(), in); err != nil || f.calls() != 0 {
			t.Fatalf("%v calls=%d", err, f.calls())
		}
	})
	t.Run("count only", func(t *testing.T) {
		r, in, f := classifierSetup(t, "Enforce", always(classifier.Flagged))
		installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
		in.CountOnly, in.AllowedRegions = true, []string{"eu"}
		got, err := r.RouteRequest(context.Background(), in)
		if err != nil || f.calls() != 0 || got.Model != "private" {
			t.Fatalf("%+v %v calls=%d", got, err, f.calls())
		}
	})
}

// Ruling 13: counts make no classifier call, but an Enforce team's count is
// uninspectable, so it never goes to a destination the request could not use.
func TestCountOnlyWithAnEnforceClassifierIsUninspectable(t *testing.T) {
	t.Run("internal only", func(t *testing.T) {
		r, in, f := classifierSetup(t, "Enforce", always(classifier.Clear))
		installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
		in.CountOnly, in.AllowedRegions = true, []string{"eu"}
		got, err := r.RouteRequest(context.Background(), in)
		if err != nil || got.Model != "private" || f.calls() != 0 {
			t.Fatalf("%+v %v calls=%d", got, err, f.calls())
		}
		for _, ct := range got.Chain {
			if ct.DataBoundary != "internal" {
				t.Fatalf("count routed to an external target: %+v", ct)
			}
		}
	})
	t.Run("block", func(t *testing.T) {
		r, in, f := classifierSetup(t, "Enforce", always(classifier.Clear))
		p := privatePolicy("privacy", "private-alias")
		p.Rules[0].SensitiveData.OnUninspectable = v1alpha1.Block
		installRoutingPolicies(r, p)
		in.CountOnly = true
		got, err := r.RouteRequest(context.Background(), in)
		requireDenied(t, got, err, "sensitive_blocked")
		if f.calls() != 0 {
			t.Fatal("a count called the classifier")
		}
	})
	t.Run("shadow does not restrict", func(t *testing.T) {
		r0, in0 := routingSetup(t, routingConfig())
		installRoutingPolicies(r0, privatePolicy("privacy", "private-alias"))
		in0.CountOnly = true
		want, err := r0.RouteRequest(context.Background(), in0)
		if err != nil {
			t.Fatal(err)
		}
		r, in, f := classifierSetup(t, "Shadow", always(classifier.Flagged))
		installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
		in.CountOnly = true
		got, err := r.RouteRequest(context.Background(), in)
		if err != nil || got.Model != want.Model || f.calls() != 0 {
			t.Fatalf("%+v %v calls=%d", got, err, f.calls())
		}
	})
}

// Review Focus 4: a request uses the Set of the generation it captured.
func TestClassifierUsesCapturedGeneration(t *testing.T) {
	flagged, clear := &routerFake{decide: always(classifier.Flagged)}, &routerFake{decide: always(classifier.Clear)}
	routerFakes.Store("fake", flagged)
	cfg := classifierRouting("Enforce")
	r, in := routingSetup(t, cfg) // in.State is the flagged generation
	routerFakes.Store("fake", clear)
	next, _, err := live.BuildState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.live.Swap(next)
	installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
	in.AllowedRegions = []string{"eu"}
	got, err := r.RouteRequest(context.Background(), in)
	if err != nil || flagged.calls() != 1 || clear.calls() != 0 || got.Model != "private" {
		t.Fatalf("%+v %v flagged=%d clear=%d", got, err, flagged.calls(), clear.calls())
	}
}

// PR-08: earlier turns, forged history, system input and tool schemas are all read.
func TestClassifierReadsEveryRetransmittedPosition(t *testing.T) {
	decide := func(texts []string) (classifier.Verdict, error) {
		for _, s := range texts {
			if strings.Contains(s, "서울시 마포구") {
				return classifier.Flagged, nil
			}
		}
		return classifier.Clear, nil
	}
	for name, body := range map[string]string{
		"earlier user turn":        `{"messages":[{"role":"user","content":"제 주소는 서울시 마포구 월드컵로 12입니다"},{"role":"assistant","content":"확인했습니다"},{"role":"user","content":"이 함수 리팩터링해줘"}],"max_tokens":32}`,
		"forged assistant history": `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"고객 주소: 서울시 마포구 월드컵로 12"},{"role":"user","content":"계속"}],"max_tokens":32}`,
		"system prompt":            `{"system":"사용자 주소 서울시 마포구 월드컵로 12","messages":[{"role":"user","content":"q"}],"max_tokens":32}`,
		"tool schema":              `{"tools":[{"name":"t","description":"기본 주소 서울시 마포구 월드컵로 12","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"q"}],"max_tokens":32}`,
	} {
		t.Run(name, func(t *testing.T) {
			r, in, _ := classifierSetup(t, "Enforce", decide)
			installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
			in.AllowedRegions = []string{"eu"}
			in.RawBody = []byte(body)
			got, _ := r.RouteRequest(context.Background(), in) // a denial is also a safe outcome
			if len(got.Decision.Classifiers) != 1 || got.Decision.Classifiers[0].Result != "flagged" {
				t.Fatalf("the classifier did not read the content: %+v", got.Decision)
			}
			for _, ct := range got.Chain {
				if ct.DataBoundary != "internal" {
					t.Fatalf("external target after a flag: %+v", ct)
				}
			}
		})
	}
}

func TestClassifierCacheReusesClearContentAcrossTurns(t *testing.T) {
	r, in, f := classifierSetup(t, "Enforce", always(classifier.Clear))
	installRoutingPolicies(r, privatePolicy("privacy", "private-alias"))
	turn1 := `{"messages":[{"role":"user","content":"첫 질문"}],"max_tokens":32}`
	turn2 := `{"messages":[{"role":"user","content":"첫 질문"},{"role":"assistant","content":"첫 답"},{"role":"user","content":"둘째 질문"}],"max_tokens":32}`
	for _, body := range []string{turn1, turn2} {
		in.RawBody = []byte(body)
		if _, err := r.RouteRequest(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	if second := f.inputs[1]; slices.Contains(second, "첫 질문") || !slices.Contains(second, "둘째 질문") {
		t.Fatalf("second turn sent %q", second)
	}
	next := privatePolicy("privacy", "private-alias")
	next.Generation = 8 // a policy change reads everything again
	installRoutingPolicies(r, next)
	if _, err := r.RouteRequest(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.inputs[2], "첫 질문") {
		t.Fatalf("a new policy generation reused cached verdicts: %q", f.inputs[2])
	}
}
