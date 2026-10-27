package router

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/sensitivity"
	"github.com/inferplane/inferplane/internal/testutil/inspectionfixture"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

type inspectionBenchmarkProvider struct {
	providers.Provider
	wire string
}

func (p inspectionBenchmarkProvider) Name() string {
	switch p.wire {
	case "responses":
		return "openai_responses"
	case "openai":
		return "openai_compatible"
	case "bedrock", "bedrock-converse":
		return "bedrock"
	default:
		return "anthropic"
	}
}
func (inspectionBenchmarkProvider) SupportsBedrockConverse(string) bool { return true }
func (inspectionBenchmarkProvider) SupportsIngress(string) bool         { return true }

// No network and no retained text; learned-model latency is deliberately absent.
type inspectionBenchmarkClassifier struct{}

func (inspectionBenchmarkClassifier) Windows(s []string) []string { return s }
func (inspectionBenchmarkClassifier) Score(_ context.Context, windows []string) (classifier.Scores, error) {
	v := make([]classifier.Verdict, len(windows))
	for i := range v {
		v[i] = classifier.Clear
	}
	return classifier.Scores{Verdicts: v, Sent: len(v)}, nil
}

func init() {
	for _, protocol := range inspectionfixture.Protocols {
		providers.Register("inspection-bench-"+protocol, func(providers.Config) (providers.Provider, error) {
			return inspectionBenchmarkProvider{mockprovider.New("upstream"), protocol}, nil
		})
	}
	classifier.Register("inspection-bench-clear", func(classifier.Config) (classifier.Classifier, error) {
		return inspectionBenchmarkClassifier{}, nil
	})
}

func inspectionBenchmarkRouting(b *testing.B, protocol, mode string, raw []byte) (*Router, RequestRoutingInput) {
	b.Helper()
	cfg := routingConfig()
	for name, p := range cfg.Providers {
		p.Type = "inspection-bench-" + protocol
		cfg.Providers[name] = p
	}
	for name, m := range cfg.Models {
		m.ContextWindow = 64 << 20
		m.Capabilities = []string{"tools"}
		cfg.Models[name] = m
	}
	if mode == "classifier" {
		zero := 0
		cfg.PIIClassification = &config.PIIClassificationConfig{
			MaxInputBytes: 16 << 20, Cache: &config.ClassifierCacheConfig{MaxEntries: &zero},
			Classifiers: []config.ClassifierConfig{{Name: "bench", Type: "inspection-bench-clear", Mode: "Enforce", Endpoint: "http://127.0.0.1:1", DataBoundary: "internal"}},
		}
	}
	state, _, err := live.BuildState(cfg)
	if err != nil {
		b.Fatal(err)
	}
	holder := &live.Holder{}
	holder.Swap(state)
	r := New(holder)
	chain, _, err := r.ResolveChain("premium")
	if err != nil {
		b.Fatal(err)
	}
	in := RequestRoutingInput{
		Principal:      keystore.Principal{Team: "team", AllowedModels: []string{"*"}},
		RequestedModel: "premium", Model: "premium", Protocol: protocol,
		RawBody: raw, State: state, Chain: chain,
	}
	switch mode {
	case "shadow", "enforce":
		contextMode := v1alpha1.Shadow
		if mode == "enforce" {
			contextMode = v1alpha1.Enforce
		}
		installRoutingPolicies(r, contextPolicy("context", contextMode, "economy"))
	case "strict":
		r.SetBudgetConstraintGate(func(keystore.Principal) map[string]string {
			return map[string]string{"premium": "private"}
		})
	case "internal", "classifier", "block", "mask":
		doc := privatePolicy("privacy", "private")
		if mode == "block" || mode == "mask" {
			action := v1alpha1.Block
			if mode == "mask" {
				action = v1alpha1.Mask
				r.SetRequestRedactor(sensitivity.NewRedactor())
			}
			doc.Rules[0].SensitiveData.OnDetected = action
			raw = []byte(strings.Replace(string(raw), "synthetic fixture text ", "person@example.test    ", 1))
			in.RawBody = raw
		}
		installRoutingPolicies(r, doc)
	}
	if _, err := r.RouteRequest(context.Background(), in); (err != nil) != (mode == "block") {
		b.Fatalf("invalid benchmark route (%s/%s): %v", protocol, mode, err)
	}
	return r, in
}

func BenchmarkRouteRequestInspection(b *testing.B) {
	// Balanced coverage: all protocol/body combinations without policy; all
	// policy modes at 200 KiB/1 MiB; concurrency sweeps on Responses.
	for _, protocol := range inspectionfixture.Protocols {
		for _, mode := range []string{"none", "shadow", "enforce", "strict", "internal", "block", "mask", "classifier"} {
			sizes := inspectionfixture.Sizes
			if mode != "none" {
				sizes = []int{200 << 10, 1 << 20}
			}
			for _, size := range sizes {
				concurrency := []int{1}
				if protocol == "responses" && (mode == "none" || mode == "shadow") {
					concurrency = inspectionfixture.Concurrency
				}
				for _, c := range concurrency {
					b.Run(fmt.Sprintf("%s/%s/bytes=%d/c=%d", protocol, mode, size, c), func(b *testing.B) {
						r, in := inspectionBenchmarkRouting(b, protocol, mode, inspectionfixture.Request(protocol, size))
						inspectionfixture.Run(b, len(in.RawBody), c, func() error {
							result, err := r.RouteRequest(context.Background(), in)
							if mode == "block" {
								if err == nil || len(result.Chain) != 0 {
									return fmt.Errorf("privacy benchmark must refuse")
								}
								return nil
							}
							return err
						})
					})
				}
			}
		}
	}
}
