// Package embeddingsapi implements the embeddings lane (roadmap ⑤):
// POST /v1/embeddings, OpenAI wire shape — a GOVERNED PASSTHROUGH, not a
// canonical one. Embeddings structurally don't fit the Messages-superset
// schema, so the body forwards verbatim (only the top-level model rewritten
// by the provider) and the pipeline governs around it: KeyAuth → model
// RBAC → region lock → PII egress ceiling → PreCheck (with cost
// reservation) → forward with priority fallback → Settle on
// usage.prompt_tokens (embeddings have no output tokens) → hash-chained
// audit. Providers opt in via the OPTIONAL providers.Embedder interface,
// discovered by type assertion — a provider that doesn't implement it
// simply cannot serve this lane (clean 404 per model), preserving the §8
// zero-core-diff rule for provider packages.
package embeddingsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/telemetry"
	"github.com/inferplane/inferplane/pkg/ulid"
	"github.com/inferplane/inferplane/providers"
)

const ingressName = "embeddings"
const rejectedModelLabel = "_rejected"
const maxBody = 32 << 20

// Handler serves POST /v1/embeddings.
type Handler struct {
	r             *router.Router
	aud           *audit.Writer
	gov           *governance.Governor
	metrics       *metrics.Metrics
	teamPolicy    func(team string) (keystore.TeamRecord, bool)
	usage         *telemetry.Collector           // nil-safe
	egressCeiling func(team, user string) string // nil-safe
}

// New builds the handler. aud/gov/m follow the other ingresses' nil-safety.
func New(r *router.Router, aud *audit.Writer, gov *governance.Governor, m *metrics.Metrics) *Handler {
	return &Handler{r: r, aud: aud, gov: gov, metrics: m}
}

func (h *Handler) SetTeamPolicy(fn func(team string) (keystore.TeamRecord, bool)) {
	h.teamPolicy = fn
}
func (h *Handler) SetUsageCollector(c *telemetry.Collector)           { h.usage = c }
func (h *Handler) SetEgressCeiling(fn func(team, user string) string) { h.egressCeiling = fn }

func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	raw, err := io.ReadAll(io.LimitReader(req.Body, maxBody))
	if err != nil {
		writeErr(w, 400, "could not read request body")
		return
	}
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Model == "" {
		writeErr(w, 400, "malformed JSON or missing model")
		return
	}
	model, substituted := h.r.ResolveModel(parsed.Model)
	if substituted {
		w.Header().Set("x-inferplane-model-fallback", model)
	}
	p, ok := principal.From(req.Context())
	if !ok {
		writeErr(w, 401, "no principal")
		return
	}
	// No tier/user-pool substitution here on purpose: budget-tier ladders
	// and premium pools substitute CHAT models; substituting an embeddings
	// model for a chat target (or vice versa) would be a category error.
	if !h.r.Allows(p, model) {
		h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: 403, Error: audit.DenyModelNotAllowed.Ptr()})
		h.metrics.ObserveRequest(ingressName, rejectedModelLabel, "", p.Team, 403, time.Since(start).Seconds(), 0)
		writeErr(w, 403, "model not allowed for this key: "+model)
		return
	}
	chain, st, err := h.r.ResolveChain(model)
	if err != nil {
		h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: 404})
		h.metrics.ObserveRequest(ingressName, rejectedModelLabel, "", p.Team, 404, time.Since(start).Seconds(), 0)
		writeErr(w, 404, "unknown model: "+model)
		return
	}
	// RBAC re-check after routing (the module invariant): a cross-model
	// fallback target appended by ResolveChain is unchecked until here.
	chain = router.FilterModelAllowed(chain, func(m string) bool { return h.r.Allows(p, m) })
	var teamRec keystore.TeamRecord
	if h.teamPolicy != nil {
		if rec, ok := h.teamPolicy(p.Team); ok {
			teamRec = rec
		}
	}
	if len(teamRec.AllowedRegions) > 0 {
		if filtered := router.FilterRegions(chain, teamRec.AllowedRegions); len(filtered) == 0 {
			h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: 403, Error: audit.DenyRegionBlocked.Ptr()})
			h.metrics.ObserveRequest(ingressName, rejectedModelLabel, "", p.Team, 403, time.Since(start).Seconds(), 0)
			writeErr(w, 403, "no allowed-region target for model: "+model)
			return
		} else {
			chain = filtered
		}
	}
	// PII egress ceiling (strategy Phase 2), same resolved-chain point as
	// everywhere. Embeddings input is raw text leaving the boundary, so the
	// ceiling applies in full — and this lane has no masker or detector, so
	// external-masked and external-unmodified REFUSE (fail closed), the same
	// posture the chat ingress takes for its unverifiable cases.
	if h.egressCeiling != nil {
		switch h.egressCeiling(p.Team, identityOf(p)) {
		case "blocked":
			h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: 403, Error: audit.DenyPIIBlocked.Ptr()})
			h.metrics.ObserveRequest(ingressName, rejectedModelLabel, "", p.Team, 403, time.Since(start).Seconds(), 0)
			writeErr(w, 403, "embedding requests are blocked for this subject by PII policy")
			return
		case "internal-only":
			if filtered := router.FilterInternal(chain); len(filtered) == 0 {
				h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: 403, Error: audit.DenyPIINoInternalTarget.Ptr()})
				h.metrics.ObserveRequest(ingressName, rejectedModelLabel, "", p.Team, 403, time.Since(start).Seconds(), 0)
				writeErr(w, 403, "PII policy restricts this subject to internal providers and no internal target serves model: "+model)
				return
			} else {
				chain = filtered
			}
		case "external-masked":
			h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: 403, Error: audit.DenyPIIMaskUnavailable.Ptr()})
			h.metrics.ObserveRequest(ingressName, rejectedModelLabel, "", p.Team, 403, time.Since(start).Seconds(), 0)
			writeErr(w, 403, "PII policy mandates masking for this subject and the embeddings lane has no masker — refusing to send unmasked text externally")
			return
		case "external-unmodified":
			h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: 403, Error: audit.DenyPIIDetectorUnavailable.Ptr()})
			h.metrics.ObserveRequest(ingressName, rejectedModelLabel, "", p.Team, 403, time.Since(start).Seconds(), 0)
			writeErr(w, 403, "PII policy requires detector-verified unmodified egress, which the embeddings lane cannot verify — refusing")
			return
		}
	}
	table := st.Pricing()
	if dec := governance.PricingGuard(table, pricedTargets(chain)); !dec.Allowed {
		h.audit(req.Context(), p, model, chain[0].Upstream, &audit.OutcomeRef{Status: dec.Status, Error: dec.Code.Ptr()})
		h.metrics.ObserveRequest(ingressName, model, chain[0].ProviderName, p.Team, dec.Status, time.Since(start).Seconds(), 0)
		writeErr(w, dec.Status, dec.Reason)
		return
	}
	// Reserve/settle upper bound: input only — embeddings have no output
	// tokens, so max_tokens contributes nothing.
	estTokens := estimateTokens(raw)
	estCost := governance.CostUpperBound(table, chain[0].ProviderName, chain[0].Upstream, estTokens, 0)
	if h.gov != nil {
		dec := h.gov.PreCheckCost(subjectOf(p), keyPolicyOf(p), estTokens, estCost)
		if !dec.Allowed {
			h.audit(req.Context(), p, model, chain[0].Upstream, &audit.OutcomeRef{Status: dec.Status, Error: dec.Code.Ptr()})
			h.metrics.ObserveRequest(ingressName, model, chain[0].ProviderName, p.Team, dec.Status, time.Since(start).Seconds(), 0)
			writeErr(w, dec.Status, dec.Reason)
			return
		}
	}
	h.audit(req.Context(), p, model, chain[0].Upstream, nil)

	// Priority fallback over EMBEDDER targets only: a chain target whose
	// provider doesn't implement the optional interface is skipped, and a
	// chain with none is a clean 404 (the design's "anthropic simply doesn't
	// implement it" case).
	sawEmbedder := false
	for i, ct := range chain {
		emb, ok := ct.Provider.(providers.Embedder)
		if !ok {
			continue
		}
		sawEmbedder = true
		last := i == len(chain)-1
		resp, err := emb.Embed(req.Context(), &providers.EmbedRequest{
			Model: ct.Model, Upstream: ct.Upstream, RawBody: raw, Headers: req.Header.Clone(),
		})
		if err != nil {
			h.r.RecordResult(ct.ProviderName, ct.Identity, false)
			if !last {
				w.Header().Set("x-inferplane-fallback", "true")
				continue
			}
			h.auditCompleted(req.Context(), p, model, ct.Upstream, 502, nil, nil)
			h.metrics.ObserveRequest(ingressName, model, ct.ProviderName, p.Team, 502, time.Since(start).Seconds(), 0)
			writeErr(w, 502, "upstream error")
			return
		}
		// Zero-bill fence (Phase 0a, the ADR-030 class): a 2xx whose usage
		// cannot be accounted must not be served free.
		if resp.StatusCode/100 == 2 && resp.PromptTokens <= 0 {
			h.r.RecordResult(ct.ProviderName, ct.Identity, false)
			if !last {
				w.Header().Set("x-inferplane-fallback", "true")
				continue
			}
			h.auditCompleted(req.Context(), p, model, ct.Upstream, 502, nil, nil)
			h.metrics.ObserveRequest(ingressName, model, ct.ProviderName, p.Team, 502, time.Since(start).Seconds(), 0)
			writeErr(w, 502, "upstream returned a success the gateway cannot account (no usage) — refusing to serve it unbilled")
			return
		}
		if resp.StatusCode/100 != 2 && !last {
			h.r.RecordResult(ct.ProviderName, ct.Identity, false)
			w.Header().Set("x-inferplane-fallback", "true")
			continue
		}
		// Tee verbatim (incl. a final-target non-2xx error body).
		if ct := resp.Headers.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(resp.RawBody)
		if resp.StatusCode/100 != 2 {
			h.auditCompleted(req.Context(), p, model, ct.Upstream, resp.StatusCode, nil, nil)
			h.metrics.ObserveRequest(ingressName, model, ct.ProviderName, p.Team, resp.StatusCode, time.Since(start).Seconds(), 0)
			return
		}
		h.r.RecordResult(ct.ProviderName, ct.Identity, true)
		pu := pricing.Usage{Input: resp.PromptTokens}
		var cost *audit.CostRef
		if h.gov != nil {
			c, missing := h.gov.SettleCost(subjectOf(p), keyPolicyOf(p), ct.ProviderName, ct.Upstream, pu, table, estTokens, estCost)
			cost = &audit.CostRef{AmountUSDMicros: c, PricingMissing: missing, PricingVersion: governance.PricingVersionOf(table)}
			if h.usage != nil {
				h.usage.Record(p.Team, identityOf(p), ct.Upstream, pu, c)
			}
		}
		h.metrics.ObserveTokenUsage("input", model, ct.ProviderName, p.Team, resp.PromptTokens)
		usage := &audit.UsageRef{InputTokens: resp.PromptTokens}
		h.auditCompleted(req.Context(), p, model, ct.Upstream, resp.StatusCode, usage, cost)
		h.metrics.ObserveRequest(ingressName, model, ct.ProviderName, p.Team, resp.StatusCode, time.Since(start).Seconds(), 0)
		return
	}
	status, msg := 404, "no embeddings-capable provider serves model: "+model
	if sawEmbedder {
		status, msg = 502, "all embeddings targets failed for model: "+model
	}
	h.auditCompleted(req.Context(), p, model, "", status, nil, nil)
	h.metrics.ObserveRequest(ingressName, model, "", p.Team, status, time.Since(start).Seconds(), 0)
	writeErr(w, status, msg)
}

// --- per-package helper duplication (the keyPolicyOf convention: these stay
// package-local so governance remains a leaf) ---

func subjectOf(p keystore.Principal) governance.Subject {
	user := p.UserID
	if user == "" {
		user = p.Owner
	}
	return governance.Subject{Team: p.Team, KeyID: p.KeyID, User: user}
}

func identityOf(p keystore.Principal) string {
	if p.UserID != "" {
		return p.UserID
	}
	return p.Owner
}

func keyPolicyOf(p keystore.Principal) governance.KeyPolicy {
	return governance.KeyPolicy{
		RatePerMin:           p.RPM,
		TokensPerMinute:      p.TPM,
		BudgetMicrosPerMonth: p.BudgetUSDMicros,
		BudgetMicrosPerDay:   p.BudgetUSDMicrosPerDay,
	}
}

// estimateTokens mirrors the chat ingresses' conservative bytes/4 estimate.
func estimateTokens(raw []byte) int64 {
	est := int64(len(raw) / 4)
	if est < 1 {
		est = 1
	}
	return est
}

func pricedTargets(chain []router.ChainTarget) []governance.PricedTarget {
	out := make([]governance.PricedTarget, len(chain))
	for i, ct := range chain {
		out[i] = governance.PricedTarget{Provider: ct.ProviderName, Upstream: ct.Upstream}
	}
	return out
}

func (h *Handler) audit(ctx context.Context, p keystore.Principal, model, upstream string, outcome *audit.OutcomeRef) {
	if h.aud == nil {
		return
	}
	rec := audit.Record{
		SchemaVersion: 1,
		Event:         "request_started",
		ID:            ulid.New(),
		TS:            time.Now().UTC().Format(time.RFC3339Nano),
		Principal:     audit.PrincipalRef{KeyID: p.KeyID, Team: p.Team, UserID: p.UserID},
		Request:       audit.RequestRef{Ingress: ingressName, ModelRequested: model, ModelResolved: upstream},
		Outcome:       outcome,
	}
	h.aud.Append(rec)
}

func (h *Handler) auditCompleted(ctx context.Context, p keystore.Principal, model, upstream string, status int, usage *audit.UsageRef, cost *audit.CostRef) {
	if h.aud == nil {
		return
	}
	rec := audit.Record{
		SchemaVersion: 1,
		Event:         "request_completed",
		ID:            ulid.New(),
		TS:            time.Now().UTC().Format(time.RFC3339Nano),
		Principal:     audit.PrincipalRef{KeyID: p.KeyID, Team: p.Team, UserID: p.UserID},
		Request:       audit.RequestRef{Ingress: ingressName, ModelRequested: model, ModelResolved: upstream},
		Outcome:       &audit.OutcomeRef{Status: status},
		Usage:         usage,
		Cost:          cost,
	}
	h.aud.Append(rec)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": errType(status)},
	})
}

func errType(status int) string {
	switch status {
	case 401:
		return "authentication_error"
	case 402, 429:
		return "rate_limit_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	default:
		return "invalid_request_error"
	}
}
