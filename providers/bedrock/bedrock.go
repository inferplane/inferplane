package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

func init() { providers.Register("bedrock", factory) }

// factory builds a bedrock provider from registry config. region/auth_mode/
// profile come from the per-provider config (main.go fills Settings); model_api
// is an optional JSON map {upstreamModelID: api} gathered from the model targets
// pointing at this provider, used to override the default invoke/converse
// routing. The real AWS client is constructed here — newAWSClient loads the
// default config offline (it does not validate credentials), so registration is
// always exercised in tests.
func factory(cfg providers.Config) (providers.Provider, error) {
	region := cfg.Settings["region"]
	authMode := cfg.Settings["auth_mode"]
	profile := cfg.Settings["profile"]
	ac, err := newAWSClient(context.Background(), region, authMode, profile, cfg.Credentials)
	if err != nil {
		return nil, fmt.Errorf("bedrock: aws config: %w", err)
	}
	modelAPI := map[string]string{}
	if raw := cfg.Settings["model_api"]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &modelAPI)
	}
	// Mantle base URL is derived from the region; Settings["mantle_base_url"]
	// overrides it (tests, private endpoints).
	mantleBase := cfg.Settings["mantle_base_url"]
	if mantleBase == "" {
		mantleBase = fmt.Sprintf("https://bedrock-mantle.%s.api.aws", ac.cfg.Region)
	}
	// awsClient implements both invoker and converser; the mantle client
	// shares its resolved credentials.
	return &provider{
		inv: ac, conv: ac, modelAPI: modelAPI,
		man:              newMantleClient(mantleBase, ac.cfg.Region, ac.cfg.Credentials, cfg.HTTPClient),
		defaultGuardrail: Guardrail{ID: cfg.Settings["guardrail_id"], Version: cfg.Settings["guardrail_version"]},
	}, nil
}

type provider struct {
	inv              invoker
	conv             converser
	man              mantler
	modelAPI         map[string]string // upstream modelId → "invoke_model"|"converse"|"mantle"
	defaultGuardrail Guardrail         // provider-level default (D6, ADR-019) — the anti-bypass fix
}

// guardrailFor resolves the effective guardrail for one request: a per-team
// override (req.GuardrailID, threaded from the team record via
// providers.ProxyRequest) wins over the provider's configured default. There
// is deliberately no opt-out — a team can select a DIFFERENT guardrail, never
// remove the default one (ADR-019).
func (p *provider) guardrailFor(req *providers.ProxyRequest) Guardrail {
	if req.GuardrailID != "" {
		return Guardrail{ID: req.GuardrailID, Version: req.GuardrailVersion}
	}
	return p.defaultGuardrail
}

func (p *provider) Name() string               { return "bedrock" }
func (p *provider) Models() []schema.ModelInfo { return nil }

// apiFor decides invoke vs converse vs mantle. Default: Claude models →
// invoke_model, others → converse. Explicit per-model config overrides.
// "mantle" routes to the Mantle endpoint (mantle.go) — the former
// invoke_model fallback (M4/§10 #2) silently sent Mantle-only models
// (openai.gpt-5.4/-5.5) to an endpoint that has never heard of them.
// egressAPIs enumerates every egress path the Complete/Stream dispatch
// below can route to. The guardrail-coverage fence
// (guardrail_fence_test.go) iterates this list and proves each path either
// APPLIES the effective guardrail on the upstream call or REFUSES a
// guarded request before egress — adding a new case to the dispatch
// switches without extending both this list and the fence is the
// regression the strategy's "Guardrail / residency" P0 exists to prevent.
var egressAPIs = []string{"invoke_model", "converse", "mantle"}

func (p *provider) apiFor(upstream string) string {
	if a, ok := p.modelAPI[upstream]; ok && a != "" {
		return a
	}
	if strings.Contains(upstream, "anthropic.") || strings.Contains(upstream, "claude") {
		return "invoke_model"
	}
	return "converse"
}

// mantleGuardrailCheck refuses a request whose effective guardrail cannot be
// enforced on the selected egress path. Mantle has no guardrail parameter at
// all, so routing a guarded model through it would send the request unguarded —
// and because the ingress writes ProxyRequest.GuardrailID into the
// tamper-evident audit chain unconditionally, the record would then ATTEST a
// guardrail that never ran. A falsified attestation is worse than a bypass, and
// ADR-019 gives guardrails no per-team opt-out, so `routing.model_api: mantle`
// must not become one: refuse instead of serving unguarded.
func (p *provider) mantleGuardrailCheck(req *providers.ProxyRequest) error {
	if g := p.guardrailFor(req); g.ID != "" {
		return synthError(400, "bedrock: guardrail cannot be enforced on the mantle egress path for this model — remove the guardrail or route the model via converse/invoke_model")
	}
	return nil
}

func (p *provider) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	switch p.apiFor(req.Upstream) {
	case "converse":
		return p.completeConverse(ctx, req)
	case "mantle":
		if err := p.mantleGuardrailCheck(req); err != nil {
			return nil, err
		}
		return p.man.Complete(ctx, req)
	default:
		return p.completeInvoke(ctx, req)
	}
}

func (p *provider) Stream(ctx context.Context, req *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	switch p.apiFor(req.Upstream) {
	case "converse":
		return p.streamConverse(ctx, req)
	case "mantle":
		if err := p.mantleGuardrailCheck(req); err != nil {
			return nil, err
		}
		return p.man.Stream(ctx, req)
	default:
		return p.streamInvoke(ctx, req)
	}
}

var _ providers.Provider = (*provider)(nil)
