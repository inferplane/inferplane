// Package bedrockapi implements the AWS Bedrock InvokeModel ingress.
package bedrockapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/bodystore"
	"github.com/inferplane/inferplane/internal/filter"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/telemetry"
	"github.com/inferplane/inferplane/internal/tracing"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/pkg/ulid"
	"github.com/inferplane/inferplane/providers"
	"go.opentelemetry.io/otel/trace"
)

const (
	ingressName        = "bedrock"
	rejectedModelLabel = "_rejected"
)

type InvokeHandler struct {
	r             *router.Router
	holder        *live.Holder
	aud           *audit.Writer
	gov           *governance.Governor
	metrics       *metrics.Metrics
	mask          *filter.Masking
	teamPolicy    func(team string) (keystore.TeamRecord, bool)
	bodies        *bodystore.Recorder
	usage         *telemetry.Collector           // nil-safe: usage telemetry off when nil (control-plane mode)
	egressCeiling func(team, user string) string // nil-safe: PII egress ceiling off when nil (strategy Phase 2)
	streaming     bool
}

func NewInvokeHandler(r *router.Router, holder *live.Holder, streaming bool) *InvokeHandler {
	return &InvokeHandler{r: r, holder: holder, streaming: streaming}
}

func NewInvokeHandlerMetrics(r *router.Router, holder *live.Holder, aud *audit.Writer, gov *governance.Governor, m *metrics.Metrics, streaming bool) *InvokeHandler {
	return &InvokeHandler{
		r: r, holder: holder, aud: aud, gov: gov, metrics: m, streaming: streaming,
	}
}

func (h *InvokeHandler) SetMasking(m *filter.Masking) { h.mask = m }

func (h *InvokeHandler) SetTeamPolicy(fn func(team string) (keystore.TeamRecord, bool)) {
	h.teamPolicy = fn
}

// SetUsageCollector enables per-request usage telemetry (control-plane mode);
// nil-safe, mirrors anthropicapi.
func (h *InvokeHandler) SetUsageCollector(c *telemetry.Collector) { h.usage = c }

func (h *InvokeHandler) SetBodyRecorder(r *bodystore.Recorder) { h.bodies = r }

// SetEgressCeiling installs the PII egress-ceiling lookup (strategy Phase 2);
// nil-safe, mirrors anthropicapi.
func (h *InvokeHandler) SetEgressCeiling(fn func(team, user string) string) { h.egressCeiling = fn }

func (h *InvokeHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read request body")
		return
	}

	// Parsed is observation-only. Bedrock requests are always forwarded from
	// RawBody so unknown fields and prompt-cache bytes remain untouched.
	var parsed schema.ChatRequest
	_ = json.Unmarshal(raw, &parsed)

	urlID := req.PathValue("modelId")
	model, substituted, resolved := resolveModel(h.r, h.holder, urlID)
	if substituted {
		w.Header().Set("x-inferplane-model-fallback", model)
	}

	tctx := tracing.Extract(req.Context(), req.Header)
	tctx, span := tracing.Start(tctx, "invoke "+model)
	defer span.End()
	req = req.WithContext(tctx)
	tracing.SetGenAIRequest(span, model)
	traceID := tracing.TraceID(tctx)

	p, ok := principal.From(req.Context())
	if !ok {
		tracing.SetStatus(span, false, "no principal")
		writeErr(w, http.StatusUnauthorized, "no principal")
		return
	}
	if !resolved {
		h.audit(req.Context(), p, urlID, "", &audit.OutcomeRef{Status: http.StatusNotFound}, false, traceID)
		h.metrics.ObserveRequest(ingressName, rejectedModelLabel, "", p.Team, http.StatusNotFound, time.Since(start).Seconds(), 0)
		tracing.SetStatus(span, false, "unknown model")
		writeErr(w, http.StatusNotFound, "model not found")
		return
	}
	// ADR-041: budget-tier substitution — see anthropicapi's identical seam
	// for the full rationale. Native Bedrock ingress hand-rolls resolution
	// (resolveModel above, not router.ResolveModel) so it needs its own call
	// site, or it would be a cost-leak path around the same policy the other
	// two ingresses enforce.
	if served, tierSubstituted := h.r.SubstituteTier(p, model); tierSubstituted {
		// ADR-041: substitution never denies. SubstituteTier checks the
		// target is routed and allowed in general, but not that any of its
		// providers serves THIS ingress — committing such a target would
		// turn a served request into a 404 below the moment the budget tier
		// activates. An unservable target means the substitution is ignored,
		// not the request denied.
		if servableOnBedrockIngress(h.r, served) {
			h.metrics.ObserveModelSubstitution(p.Team, model, served)
			req = req.WithContext(audit.WithSubstitutedFrom(req.Context(), model))
			model = served
			tracing.SetGenAIRequest(span, model)
			w.Header().Set("x-inferplane-substituted-model", model)
		}
	}
	// Two-pool user budget (Phase 1 spec), after the team tier. On this
	// ingress a pool fallback must ALSO be servable here; an exhausted pool
	// whose first-compatible target cannot serve the Bedrock ingress fails
	// CLOSED (402) — the premium model is never served past the pool, and
	// silently ignoring the pool would be a money leak, the opposite of the
	// tier rule above (which protects availability, not a ceiling).
	if served, poolSubstituted, poolBlocked := h.r.ApplyUserPool(p, model); poolBlocked || (poolSubstituted && !servableOnBedrockIngress(h.r, served)) {
		h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: http.StatusPaymentRequired, Error: audit.DenyUserBudgetExceeded.Ptr()}, false, traceID)
		h.metrics.ObserveRequest(ingressName, model, "", p.Team, http.StatusPaymentRequired, time.Since(start).Seconds(), 0)
		tracing.SetStatus(span, false, "user premium budget exhausted")
		writeErr(w, http.StatusPaymentRequired, "user premium budget exhausted and no approved fallback model is available for this key")
		return
	} else if poolSubstituted {
		h.metrics.ObserveModelSubstitution(p.Team, model, served)
		req = req.WithContext(audit.WithSubstitutedFrom(req.Context(), model))
		model = served
		tracing.SetGenAIRequest(span, model)
		w.Header().Set("x-inferplane-substituted-model", model)
	}
	if !h.r.Allows(p, model) {
		h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: http.StatusForbidden, Error: audit.DenyModelNotAllowed.Ptr()}, false, traceID)
		h.metrics.ObserveRequest(ingressName, model, "", p.Team, http.StatusForbidden, time.Since(start).Seconds(), 0)
		tracing.SetStatus(span, false, "model not allowed")
		writeErr(w, http.StatusForbidden, "model not allowed for this key")
		return
	}

	chain, st, err := h.r.ResolveChain(model)
	if err != nil {
		h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: http.StatusNotFound}, false, traceID)
		h.metrics.ObserveRequest(ingressName, rejectedModelLabel, "", p.Team, http.StatusNotFound, time.Since(start).Seconds(), 0)
		tracing.SetStatus(span, false, "unknown model")
		writeErr(w, http.StatusNotFound, "model not found")
		return
	}
	// ResolveChain may have appended a cross-model fallback's targets (D5)
	// AFTER the allow-list check above already ran against `model` alone —
	// re-check those targets' model or a key allowed only `model` would
	// silently reach the fallback model.
	chain = router.FilterModelAllowed(chain, func(m string) bool { return h.r.Allows(p, m) })
	filtered := make([]router.ChainTarget, 0, len(chain))
	for _, ct := range chain {
		if servesBedrockIngress(ct.Provider.Name()) {
			filtered = append(filtered, ct)
		}
	}
	if len(filtered) == 0 {
		h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: http.StatusNotFound}, false, traceID)
		h.metrics.ObserveRequest(ingressName, model, "", p.Team, http.StatusNotFound, time.Since(start).Seconds(), 0)
		tracing.SetStatus(span, false, "no bedrock target")
		writeErr(w, http.StatusNotFound, "model not found")
		return
	}
	chain = filtered

	var teamRec keystore.TeamRecord
	if h.teamPolicy != nil {
		if rec, ok := h.teamPolicy(p.Team); ok {
			teamRec = rec
		}
	}
	if len(teamRec.AllowedRegions) > 0 {
		if filtered := router.FilterRegions(chain, teamRec.AllowedRegions); len(filtered) == 0 {
			h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: http.StatusForbidden, Error: audit.DenyRegionBlocked.Ptr()}, false, traceID)
			h.metrics.ObserveRequest(ingressName, model, "", p.Team, http.StatusForbidden, time.Since(start).Seconds(), 0)
			tracing.SetStatus(span, false, "region blocked")
			writeErr(w, http.StatusForbidden, "no allowed-region target for model")
			return
		} else {
			chain = filtered
		}
	}

	// PII egress ceiling (strategy Phase 2): enforced fail-closed at the
	// SAME point as the region lock — on the fully resolved chain, so
	// budget substitution and provider fallback cannot route around it.
	if h.egressCeiling != nil {
		switch h.egressCeiling(p.Team, identityOf(p)) {
		case "blocked":
			h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: http.StatusForbidden, Error: audit.DenyPIIBlocked.Ptr()}, false, traceID)
			h.metrics.ObserveRequest(ingressName, model, "", p.Team, http.StatusForbidden, time.Since(start).Seconds(), 0)
			tracing.SetStatus(span, false, "pii blocked")
			writeErr(w, http.StatusForbidden, "generation requests are blocked for this subject by PII policy")
			return
		case "internal-only":
			if filtered := router.FilterInternal(chain); len(filtered) == 0 {
				h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: http.StatusForbidden, Error: audit.DenyPIINoInternalTarget.Ptr()}, false, traceID)
				h.metrics.ObserveRequest(ingressName, model, "", p.Team, http.StatusForbidden, time.Since(start).Seconds(), 0)
				tracing.SetStatus(span, false, "pii internal-only")
				writeErr(w, http.StatusForbidden, "PII policy restricts this subject to internal providers and no internal target serves model: "+model)
				return
			} else {
				chain = filtered
			}
		case "external-masked":
			if !h.mask.Enabled(p.Team) {
				h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: http.StatusForbidden, Error: audit.DenyPIIMaskUnavailable.Ptr()}, false, traceID)
				h.metrics.ObserveRequest(ingressName, model, "", p.Team, http.StatusForbidden, time.Since(start).Seconds(), 0)
				tracing.SetStatus(span, false, "pii mask unavailable")
				writeErr(w, http.StatusForbidden, "PII policy mandates masking for this subject but the masking filter is not active for this team — refusing to send unmasked text externally")
				return
			}
		case "external-unmodified":
			// Requires a COMPLETED detector chain reporting nothing
			// protected: the detector is the SAME maskBody pass masking
			// uses, run detect-only (output discarded) so detection and
			// transformation can never disagree. No detector, a detector
			// error, or a hit all refuse — fail closed, per the contract.
			if h.mask == nil || h.mask.Filter == nil {
				h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: http.StatusForbidden, Error: audit.DenyPIIDetectorUnavailable.Ptr()}, false, traceID)
				h.metrics.ObserveRequest(ingressName, model, "", p.Team, http.StatusForbidden, time.Since(start).Seconds(), 0)
				tracing.SetStatus(span, false, "pii detector unavailable")
				writeErr(w, http.StatusForbidden, "PII policy requires detector-verified unmodified egress but no detector filter is configured — refusing")
				return
			}
			_, n, derr := maskBody(raw, h.mask.Filter)
			det := filter.Detection{Redactions: n}
			if derr != nil || !det.Clean() {
				h.audit(req.Context(), p, model, "", &audit.OutcomeRef{Status: http.StatusForbidden, Error: audit.DenyPIIProtectedDetected.Ptr()}, false, traceID)
				h.metrics.ObserveRequest(ingressName, model, "", p.Team, http.StatusForbidden, time.Since(start).Seconds(), 0)
				tracing.SetStatus(span, false, "pii protected detected")
				writeErr(w, http.StatusForbidden, "PII policy allows only detector-verified unmodified egress and the detector reported protected content — refusing")
				return
			}
		}
	}
	piiMasked := false
	if h.mask.Enabled(p.Team) {
		masked, n, err := maskBody(raw, h.mask.Filter)
		if err != nil {
			h.audit(req.Context(), p, model, chain[0].Upstream, &audit.OutcomeRef{Status: http.StatusBadRequest}, false, traceID)
			h.metrics.ObserveRequest(ingressName, model, chain[0].ProviderName, p.Team, http.StatusBadRequest, time.Since(start).Seconds(), 0)
			tracing.SetStatus(span, false, "pii mask failed")
			writeErr(w, http.StatusBadRequest, "request could not be PII-masked")
			return
		}
		if n > 0 {
			var reparsed schema.ChatRequest
			if err := json.Unmarshal(masked, &reparsed); err != nil {
				h.audit(req.Context(), p, model, chain[0].Upstream, &audit.OutcomeRef{Status: http.StatusBadRequest}, false, traceID)
				h.metrics.ObserveRequest(ingressName, model, chain[0].ProviderName, p.Team, http.StatusBadRequest, time.Since(start).Seconds(), 0)
				tracing.SetStatus(span, false, "pii mask failed")
				writeErr(w, http.StatusBadRequest, "request could not be PII-masked")
				return
			}
			raw = masked
			parsed = reparsed
			piiMasked = true
			h.metrics.ObservePIIMask(p.Team, n)
		}
	}

	table := st.Pricing()
	// Pricing guard (ADR-030): with pricing.on_missing "block", refuse a
	// request whose resolved targets have no rate rather than serving it and
	// billing 0. Covers the routes boot validation cannot see (UI-write
	// models, fallback-appended targets). Same table used to settle below.
	// NOT gated on h.gov: on_missing "block" is a pricing setting, and a
	// deployment with governance off would otherwise serve unpriced traffic free.
	if dec := governance.PricingGuard(table, pricedTargets(chain)); !dec.Allowed {
		h.audit(req.Context(), p, model, chain[0].Upstream, &audit.OutcomeRef{Status: dec.Status, Error: dec.Code.Ptr()}, false, traceID)
		h.metrics.ObserveRequest(ingressName, model, chain[0].ProviderName, p.Team, dec.Status, time.Since(start).Seconds(), 0)
		tracing.SetStatus(span, false, "pricing missing")
		writeErr(w, dec.Status, dec.Reason)
		return
	}
	// Cost upper bound for reserve/settle (strategy Phase 1) — see the
	// anthropicapi twin for the full rationale: estimated input plus the
	// request's own max_tokens of output at the FIRST target's rate,
	// computed once so hold and release always match.
	estCost := governance.CostUpperBound(table, chain[0].ProviderName, chain[0].Upstream, estimateTokens(raw), deref(parsed.MaxTokens))
	if h.gov != nil {
		dec := h.gov.PreCheckCost(subjectOf(p), keyPolicyOf(p), estimateTokens(raw), estCost)
		if !dec.Allowed {
			h.audit(req.Context(), p, model, chain[0].Upstream, &audit.OutcomeRef{Status: dec.Status, Error: dec.Code.Ptr()}, piiMasked, traceID)
			h.metrics.ObserveRequest(ingressName, model, chain[0].ProviderName, p.Team, dec.Status, time.Since(start).Seconds(), 0)
			tracing.SetStatus(span, false, "governance deny")
			writeErr(w, dec.Status, dec.Reason)
			return
		}
	}

	h.audit(req.Context(), p, model, chain[0].Upstream, nil, piiMasked, traceID)

	for i, ct := range chain {
		upHeaders := req.Header.Clone()
		tracing.Inject(req.Context(), upHeaders)
		pr := &providers.ProxyRequest{
			Model: ct.Model, Upstream: ct.Upstream, Parsed: &parsed,
			RawBody: raw, Headers: upHeaders, Stream: h.streaming,
			IngressProtocol:  "bedrock",
			GuardrailID:      teamRec.GuardrailID,
			GuardrailVersion: teamRec.GuardrailVersion,
		}
		last := i == len(chain)-1
		// crossModelNext: the next target (if any) serves a different model
		// than this one — a D5 model-level fallback boundary, not just a
		// different provider for the same model.
		crossModelNext := !last && chain[i+1].Model != ct.Model
		if i > 0 {
			w.Header().Set("x-inferplane-fallback", ct.ProviderName)
			if ct.Model != model {
				w.Header().Set("x-inferplane-model-fallback", ct.Model)
			}
		}
		var retriable bool
		if h.streaming {
			retriable = h.serveStream(w, req, ct.Provider, pr, p, ct.Model, ct.ProviderName, ct.Identity, ct.Upstream, last, crossModelNext, start, table, estCost)
		} else {
			retriable = h.serveComplete(w, req, ct.Provider, pr, p, ct.Model, ct.ProviderName, ct.Identity, ct.Upstream, last, crossModelNext, start, table, estCost)
		}
		if !retriable {
			return
		}
		h.r.RecordResult(ct.ProviderName, ct.Identity, false)
		reason := "upstream_error"
		if crossModelNext {
			reason = "model_not_found"
		}
		h.metrics.ObserveFallback(ct.Model, ct.ProviderName, chain[i+1].ProviderName, reason)
	}
}

// isModelNotFound reports whether an upstream response looks like a "model
// not found" rejection rather than an unrelated client error — a plain 404,
// or a 400 whose body names a Bedrock ValidationException (Bedrock's actual
// shape for a model not enabled/available in that region). Deliberately
// narrow: only these are ever retried across a D5 model-level fallback
// boundary; any other 4xx stays a client error.
func isModelNotFound(statusCode int, body []byte) bool {
	if statusCode == http.StatusNotFound {
		return true
	}
	return statusCode == http.StatusBadRequest && bytes.Contains(body, []byte("ValidationException"))
}

func (h *InvokeHandler) serveComplete(w http.ResponseWriter, req *http.Request, prov providers.Provider, pr *providers.ProxyRequest, p keystore.Principal, model, providerName, identity, upstream string, last, crossModelNext bool, start time.Time, table *pricing.Table, estCost int64) (retriable bool) {
	pr.ParamsStripped = nil // per-attempt: a failed target's strips must not leak into this one's disclosure
	resp, err := prov.Complete(req.Context(), pr)
	if err != nil {
		if !last {
			return true
		}
		var ue *providers.UpstreamError
		if errors.As(err, &ue) {
			st := ue.HTTPStatus()
			writeErr(w, st, upstreamErrMessage(ue.Body, "bedrock upstream error"))
			h.auditCompleted(req.Context(), ulid.New(), p, model, upstream, st, nil, nil, tracing.TraceID(req.Context()), "", pr.GuardrailID, pr.GuardrailVersion)
			recordSpanResponse(req, prov.Name(), upstream, nil, false)
			h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, st, time.Since(start).Seconds(), 0)
			return false
		}
		writeErr(w, http.StatusBadGateway, "bedrock upstream error")
		h.auditCompleted(req.Context(), ulid.New(), p, model, upstream, http.StatusBadGateway, nil, nil, tracing.TraceID(req.Context()), "", pr.GuardrailID, pr.GuardrailVersion)
		recordSpanResponse(req, prov.Name(), upstream, nil, false)
		h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, http.StatusBadGateway, time.Since(start).Seconds(), 0)
		return false
	}
	if resp == nil {
		if !last {
			return true
		}
		writeErr(w, http.StatusBadGateway, "bedrock upstream error")
		h.auditCompleted(req.Context(), ulid.New(), p, model, upstream, http.StatusBadGateway, nil, nil, tracing.TraceID(req.Context()), "", pr.GuardrailID, pr.GuardrailVersion)
		recordSpanResponse(req, prov.Name(), upstream, nil, false)
		h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, http.StatusBadGateway, time.Since(start).Seconds(), 0)
		return false
	}
	if !last && (resp.StatusCode >= http.StatusInternalServerError || resp.StatusCode == http.StatusTooManyRequests || (crossModelNext && isModelNotFound(resp.StatusCode, resp.RawBody))) {
		return true
	}
	// Phase 0a invariant (enterprise-strategy: a settled cost is mandatory
	// for every 2xx — the ADR-030 zero-cost class): a success the gateway
	// cannot parse cannot settle, and serving it would bill 0 µUSD. Fail
	// closed — next target if one exists, else a synthesized 502. The
	// typed Converse/InvokeModel paths cannot reach this state; the fence
	// exists so a future egress that re-parses JSON fails here instead of
	// re-opening the class (the Mantle Complete bug, fixed provider-side).
	if resp.StatusCode < http.StatusBadRequest && resp.Parsed == nil {
		if !last {
			return true
		}
		writeErr(w, http.StatusBadGateway, "upstream returned a success with no accountable body; refusing to serve it unbilled")
		h.auditCompleted(req.Context(), ulid.New(), p, model, upstream, http.StatusBadGateway, nil, nil, tracing.TraceID(req.Context()), "", pr.GuardrailID, pr.GuardrailVersion)
		recordSpanResponse(req, prov.Name(), upstream, nil, false)
		h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, http.StatusBadGateway, time.Since(start).Seconds(), 0)
		return false
	}
	// Strip disclosure (strategy P1 "undisclosed request mutation"): the
	// provider dropped request params the upstream rejects — say so, on the
	// wire and in the audit record.
	if len(pr.ParamsStripped) > 0 {
		w.Header().Set("x-inferplane-params-stripped", strings.Join(pr.ParamsStripped, ","))
		req = req.WithContext(audit.WithParamsStripped(req.Context(), pr.ParamsStripped))
	}
	if resp.Headers != nil {
		copyUpstreamHeaders(w.Header(), resp.Headers)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		writeErr(w, resp.StatusCode, "bedrock upstream error")
		h.auditCompleted(req.Context(), ulid.New(), p, model, upstream, resp.StatusCode, nil, nil, tracing.TraceID(req.Context()), "", pr.GuardrailID, pr.GuardrailVersion)
		recordSpanResponse(req, prov.Name(), upstream, nil, false)
		h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, resp.StatusCode, time.Since(start).Seconds(), 0)
		return false
	}

	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	var usage *audit.UsageRef
	var cost *audit.CostRef
	if resp.Parsed != nil {
		if u := resp.Parsed.Usage; u != nil {
			if u.InputTokens != nil {
				w.Header().Set("X-Amzn-Bedrock-Input-Token-Count", strconv.FormatInt(*u.InputTokens, 10))
			}
			if u.OutputTokens != nil {
				w.Header().Set("X-Amzn-Bedrock-Output-Token-Count", strconv.FormatInt(*u.OutputTokens, 10))
			}
		}
		usage = usageRef(resp.Parsed.Usage)
		cost = h.settle(p, providerName, model, upstream, resp.Parsed.Usage, table, estimateTokens(pr.RawBody), estCost)
		h.observeTokens(model, providerName, p.Team, resp.Parsed.Usage)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(resp.RawBody)
	h.r.RecordResult(providerName, identity, true)

	recID := ulid.New()
	var bodyRef string
	if h.bodies != nil {
		bodyRef = h.bodies.Capture(recID, p.Team, pr.RawBody, resp.RawBody)
	}
	h.auditCompleted(req.Context(), recID, p, model, upstream, resp.StatusCode, usage, cost, tracing.TraceID(req.Context()), bodyRef, pr.GuardrailID, pr.GuardrailVersion)
	recordSpanSettled(req, prov.Name(), upstream, usage, cost, true, false)
	h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, resp.StatusCode, time.Since(start).Seconds(), 0)
	return false
}

func (h *InvokeHandler) serveStream(w http.ResponseWriter, req *http.Request, prov providers.Provider, pr *providers.ProxyRequest, p keystore.Principal, model, providerName, identity, upstream string, last, crossModelNext bool, start time.Time, table *pricing.Table, estCost int64) (retriable bool) {
	pr.ParamsStripped = nil // per-attempt: a failed target's strips must not leak into this one's disclosure
	seq, err := prov.Stream(req.Context(), pr)
	if err != nil {
		if !last {
			return true
		}
		var ue *providers.UpstreamError
		if errors.As(err, &ue) {
			st := ue.HTTPStatus()
			writeErr(w, st, upstreamErrMessage(ue.Body, "bedrock upstream error"))
			h.auditCompleted(req.Context(), ulid.New(), p, model, upstream, st, nil, nil, tracing.TraceID(req.Context()), "", pr.GuardrailID, pr.GuardrailVersion)
			recordSpanResponse(req, prov.Name(), upstream, nil, false)
			h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, st, time.Since(start).Seconds(), 0)
			return false
		}
		writeErr(w, http.StatusBadGateway, "bedrock upstream error")
		h.auditCompleted(req.Context(), ulid.New(), p, model, upstream, http.StatusBadGateway, nil, nil, tracing.TraceID(req.Context()), "", pr.GuardrailID, pr.GuardrailVersion)
		recordSpanResponse(req, prov.Name(), upstream, nil, false)
		h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, http.StatusBadGateway, time.Since(start).Seconds(), 0)
		return false
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		h.auditCompleted(req.Context(), ulid.New(), p, model, upstream, http.StatusInternalServerError, nil, nil, tracing.TraceID(req.Context()), "", pr.GuardrailID, pr.GuardrailVersion)
		recordSpanResponse(req, prov.Name(), upstream, nil, false)
		h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, http.StatusInternalServerError, time.Since(start).Seconds(), 0)
		return false
	}

	h.r.RecordResult(providerName, identity, true)
	// Strip disclosure (strategy P1) — the provider strips before opening
	// the stream, so the fact is known pre-commit.
	if len(pr.ParamsStripped) > 0 {
		w.Header().Set("x-inferplane-params-stripped", strings.Join(pr.ParamsStripped, ","))
		req = req.WithContext(audit.WithParamsStripped(req.Context(), pr.ParamsStripped))
	}
	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Amzn-Bedrock-Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	enc := eventstream.NewEncoder()
	var usage *audit.UsageRef
	var lastUsage *schema.Usage
	var ttft float64
	// partialFinish records a stream that broke mid-flight: 200 is already
	// committed, so the failure surfaces as an exception frame on the wire
	// and a Partial audit record — mirroring anthropicapi's
	// auditCompletedPartial path, never a clean 200 completion record.
	partialFinish := func() bool {
		if writeExceptionFrame(w, enc, "internalServerException", "stream interrupted") == nil {
			flusher.Flush()
		}
		// Tokens already delivered to the client are real infrastructure
		// cost — bill them (ADR-030). Before this, a stream that broke
		// mid-flight skipped settle() entirely and everything already
		// streamed was free, with no pricing_missing flag to show it.
		partialCost := h.settle(p, providerName, model, upstream, lastUsage, table, estimateTokens(pr.RawBody), estCost)
		// …and count them, on the same usage settle() just billed (see
		// anthropicapi's twin: metering only the clean path left the token
		// counters below the billed spend for every interrupted stream).
		h.observeTokens(model, providerName, p.Team, lastUsage)
		h.auditCompletedPartial(req.Context(), p, model, upstream, usage, partialCost, tracing.TraceID(req.Context()))
		recordSpanSettled(req, prov.Name(), upstream, usage, partialCost, false, true) // committed (partial)
		h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, http.StatusOK, time.Since(start).Seconds(), ttft)
		return false
	}
	for ev, err := range seq {
		if err != nil {
			return partialFinish()
		}
		if ev == nil || ev.Chunk == nil {
			continue
		}
		chunkJSON, err := json.Marshal(ev.Chunk)
		if err != nil {
			return partialFinish()
		}
		if ttft == 0 {
			ttft = time.Since(start).Seconds()
		}
		if err := writeChunkFrame(w, enc, chunkJSON); err != nil {
			return partialFinish()
		}
		flusher.Flush()
		// FOLD every usage-bearing frame rather than overwriting (ADR-030) —
		// the InvokeModel passthrough preserves Anthropic's frame vocabulary,
		// so input and cache counts ride message_start's nested message.usage
		// while message_delta commonly carries output alone.
		if ev.Chunk.Message != nil && ev.Chunk.Message.Usage != nil {
			lastUsage = schema.MergeUsage(lastUsage, ev.Chunk.Message.Usage)
		}
		if ev.Chunk.Usage != nil {
			lastUsage = schema.MergeUsage(lastUsage, ev.Chunk.Usage)
		}
		if lastUsage != nil {
			usage = usageRef(lastUsage)
		}
	}

	cost := h.settle(p, providerName, model, upstream, lastUsage, table, estimateTokens(pr.RawBody), estCost)
	h.observeTokens(model, providerName, p.Team, lastUsage)
	recID := ulid.New()
	var bodyRef string
	if h.bodies != nil {
		bodyRef = h.bodies.Capture(recID, p.Team, pr.RawBody, nil)
	}
	h.auditCompleted(req.Context(), recID, p, model, upstream, http.StatusOK, usage, cost, tracing.TraceID(req.Context()), bodyRef, pr.GuardrailID, pr.GuardrailVersion)
	recordSpanSettled(req, prov.Name(), upstream, usage, cost, true, false)
	h.metrics.ObserveRequest(ingressName, model, providerName, p.Team, http.StatusOK, time.Since(start).Seconds(), ttft)
	return false
}

// settle runs the Governor's post-call settlement. Cache writes are TTL-tiered
// via schema.Usage.CacheWriteTiers (ADR-030) — 1h writes cost 2x the input rate
// against 5m's 1.25x, so collapsing them under-bills.
func (h *InvokeHandler) settle(p keystore.Principal, providerName, model, upstream string, u *schema.Usage, table *pricing.Table, estimatedTokens, estCost int64) *audit.CostRef {
	if h.gov == nil || u == nil {
		return nil
	}
	write5m, write1h := u.CacheWriteTiers()
	pu := pricing.Usage{
		Input:        deref(u.InputTokens),
		Output:       deref(u.OutputTokens),
		CacheRead:    deref(u.CacheReadInputTokens),
		CacheWrite5m: write5m,
		CacheWrite1h: write1h,
	}
	cost, missing := h.gov.SettleCost(subjectOf(p), keyPolicyOf(p), providerName, upstream, pu, table, estimatedTokens, estCost)
	if h.usage != nil {
		// Attribute to the UPSTREAM model — the name pricing billed.
		h.usage.Record(p.Team, identityOf(p), upstream, pu, cost)
	}
	return &audit.CostRef{
		AmountUSDMicros: cost,
		PricingMissing:  missing,
		PricingVersion:  governance.PricingVersionOf(table),
	}
}

// subjectOf maps a Principal to the governance package's Subject: the team, the
// virtual key, and the individual the key was issued to. Owner is what carries
// per-user budget enforcement (ADR-042 Phase 3) — governance skips the user
// lookup entirely when Subject.User is empty, so dropping it here would
// silently disable the feature rather than fail. Same
// deliberately-duplicated-per-package shape as keyPolicyOf below, and for the
// same reason: governance stays a leaf and does not import keystore.
func subjectOf(p keystore.Principal) governance.Subject {
	// Phase 0b-2: the durable UserID (issuer#sub) is the enforcement
	// identity when the key carries one; Owner is the bounded fallback for
	// pre-migration keys, so their budget windows keep counting unchanged.
	user := p.UserID
	if user == "" {
		user = p.Owner
	}
	return governance.Subject{Team: p.Team, KeyID: p.KeyID, User: user}
}

// identityOf is subjectOf's attribution twin: the durable UserID when the
// key carries one, else the display Owner — so control-plane usage rolls up
// per person across key rotations (Phase 0b-2).
func identityOf(p keystore.Principal) string {
	if p.UserID != "" {
		return p.UserID
	}
	return p.Owner
}

func keyPolicyOf(p keystore.Principal) governance.KeyPolicy {
	return governance.KeyPolicy{
		RatePerMin: p.RPM, TokensPerMinute: p.TPM, BudgetMicrosPerMonth: p.BudgetUSDMicros,
		BudgetMicrosPerDay: p.BudgetUSDMicrosPerDay,
	}
}

func estimateTokens(raw []byte) int64 {
	n := int64(len(raw) / 4)
	if n < 1 {
		n = 1
	}
	return n
}

// observeTokens mirrors settle()'s mapping, including the cache-write TTL split
// (ADR-030), so the metrics and the billed amount can't disagree.
func (h *InvokeHandler) observeTokens(model, provider, team string, u *schema.Usage) {
	if u == nil {
		return
	}
	write5m, write1h := u.CacheWriteTiers()
	h.metrics.ObserveTokenUsage("input", model, provider, team, deref(u.InputTokens))
	h.metrics.ObserveTokenUsage("output", model, provider, team, deref(u.OutputTokens))
	h.metrics.ObserveTokenUsage("cache_read", model, provider, team, deref(u.CacheReadInputTokens))
	h.metrics.ObserveTokenUsage("cache_write_5m", model, provider, team, write5m)
	h.metrics.ObserveTokenUsage("cache_write_1h", model, provider, team, write1h)
}

func copyUpstreamHeaders(dst http.Header, src http.Header) {
	for k, values := range src {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Length", "Transfer-Encoding", "Connection":
			continue
		}
		for _, value := range values {
			dst.Add(k, value)
		}
	}
}

func (h *InvokeHandler) audit(ctx context.Context, p keystore.Principal, model, upstream string, outcome *audit.OutcomeRef, piiMasked bool, traceID string) {
	if h.aud == nil {
		return
	}
	rec := audit.Record{
		SchemaVersion: 1,
		Event:         "request_started",
		ID:            ulid.New(),
		TS:            time.Now().UTC().Format(time.RFC3339Nano),
		Principal:     audit.PrincipalRef{KeyID: p.KeyID, Team: p.Team, UserID: p.UserID},
		Request: audit.RequestRef{
			Ingress: "bedrock", ModelRequested: model, ModelResolved: upstream,
			Stream: h.streaming, PIIMasked: piiMasked, ModelSubstitutedFrom: audit.SubstitutedFrom(ctx),
		},
		Outcome: outcome,
	}
	if traceID != "" {
		rec.TraceID = &traceID
	}
	h.aud.Append(rec)
}

func (h *InvokeHandler) auditCompleted(ctx context.Context, id string, p keystore.Principal, model, upstream string, status int, usage *audit.UsageRef, cost *audit.CostRef, traceID, bodyRef, guardrailID, guardrailVersion string) {
	if h.aud == nil {
		return
	}
	rec := audit.Record{
		SchemaVersion: 1,
		Event:         "request_completed",
		ID:            id,
		TS:            time.Now().UTC().Format(time.RFC3339Nano),
		Principal:     audit.PrincipalRef{KeyID: p.KeyID, Team: p.Team, UserID: p.UserID},
		Request: audit.RequestRef{
			Ingress: "bedrock", ModelRequested: model, ModelResolved: upstream, Stream: h.streaming,
			ModelSubstitutedFrom: audit.SubstitutedFrom(ctx),
		},
		Outcome: &audit.OutcomeRef{Status: status},
		Usage:   usage,
		Cost:    cost,
	}
	if traceID != "" {
		rec.TraceID = &traceID
	}
	if bodyRef != "" {
		rec.BodyRef = &bodyRef
	}
	if guardrailID != "" {
		rec.GuardrailID = &guardrailID
	}
	if guardrailVersion != "" {
		rec.GuardrailVersion = &guardrailVersion
	}
	h.aud.Append(rec)
}

// auditCompletedPartial records a stream that broke mid-flight: status 200 was
// already sent to the client, but the response is partial (mirrors
// anthropicapi's helper of the same name).
func (h *InvokeHandler) auditCompletedPartial(ctx context.Context, p keystore.Principal, model, upstream string, usage *audit.UsageRef, cost *audit.CostRef, traceID string) {
	if h.aud == nil {
		return
	}
	rec := audit.Record{
		SchemaVersion: 1,
		Event:         "request_completed",
		ID:            ulid.New(),
		TS:            time.Now().UTC().Format(time.RFC3339Nano),
		Principal:     audit.PrincipalRef{KeyID: p.KeyID, Team: p.Team, UserID: p.UserID},
		Request:       audit.RequestRef{Ingress: "bedrock", ModelRequested: model, ModelResolved: upstream, Stream: h.streaming, ModelSubstitutedFrom: audit.SubstitutedFrom(ctx), ParamsStripped: audit.ParamsStrippedFrom(ctx)},
		Outcome:       &audit.OutcomeRef{Status: 200, Partial: true},
		Usage:         usage,
		Cost:          cost,
	}
	if traceID != "" {
		rec.TraceID = &traceID
	}
	h.aud.Append(rec)
}

func recordSpanResponse(req *http.Request, system, upstream string, usage *audit.UsageRef, ok bool) {
	span := trace.SpanFromContext(req.Context())
	var input, output int64
	if usage != nil {
		input, output = usage.InputTokens, usage.OutputTokens
	}
	tracing.SetGenAIResponse(span, system, upstream, input, output)
	tracing.SetStatus(span, ok, "")
}

// partialSpanDesc is the fixed span-status description for an upstream-truncated
// stream — fixed because the underlying AWS SDK error string can carry an
// account id/ARN and a span export leaves the process.
const partialSpanDesc = "upstream stream interrupted"

// recordSpanSettled is recordSpanResponse for a response that reached
// settlement: cache tiers + settled µUSD cost, and a truncated stream marked
// both partial and errored (the wire status stayed 200, so the span is the only
// place a trace consumer sees the truncation). Mirrors anthropicapi.
func recordSpanSettled(req *http.Request, system, upstream string, usage *audit.UsageRef, cost *audit.CostRef, ok, partial bool) {
	span := trace.SpanFromContext(req.Context())
	var input, output, cacheRead, write5m, write1h int64
	if usage != nil {
		input, output = usage.InputTokens, usage.OutputTokens
		cacheRead = usage.CacheReadInputTokens
		write5m, write1h = usage.CacheCreation5mInputTokens, usage.CacheCreation1hInputTokens
	}
	tracing.SetGenAIResponse(span, system, upstream, input, output)
	tracing.SetUsageDetail(span, cacheRead, write5m, write1h)
	if cost != nil {
		tracing.SetCost(span, cost.AmountUSDMicros, cost.PricingMissing)
	}
	if partial {
		tracing.SetPartial(span)
		tracing.SetStatus(span, false, partialSpanDesc)
		return
	}
	tracing.SetStatus(span, ok, "")
}

// usageRef maps an observed schema.Usage to the audit UsageRef. Cache writes are
// recorded both as the total and as the 1.25x/2x TTL split, from the same
// CacheWriteTiers resolution settle() bills from — see anthropicapi's twin for
// why reading the flat field alone recorded a zero.
func usageRef(u *schema.Usage) *audit.UsageRef {
	if u == nil {
		return nil
	}
	write5m, write1h := u.CacheWriteTiers()
	return &audit.UsageRef{
		InputTokens:                deref(u.InputTokens),
		OutputTokens:               deref(u.OutputTokens),
		CacheReadInputTokens:       deref(u.CacheReadInputTokens),
		CacheCreationInputTokens:   write5m + write1h,
		CacheCreation5mInputTokens: write5m,
		CacheCreation1hInputTokens: write1h,
	}
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func maskBody(raw []byte, f filter.RequestFilter) ([]byte, int, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, 0, fmt.Errorf("maskBody: %w", err)
	}
	messagesRaw, ok := top["messages"]
	if !ok {
		return raw, 0, nil
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(messagesRaw, &messages); err != nil {
		return nil, 0, fmt.Errorf("maskBody messages: %w", err)
	}

	total := 0
	for i, messageRaw := range messages {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(messageRaw, &message); err != nil {
			return nil, 0, fmt.Errorf("maskBody message[%d]: %w", i, err)
		}
		content, ok := message["content"]
		if !ok {
			continue
		}
		masked, n, err := maskContent(content, f)
		if err != nil {
			return nil, 0, err
		}
		if n > 0 {
			message["content"] = masked
			remarshaled, err := json.Marshal(message)
			if err != nil {
				return nil, 0, fmt.Errorf("maskBody remarshal message[%d]: %w", i, err)
			}
			messages[i] = remarshaled
			total += n
		}
	}
	if total == 0 {
		return raw, 0, nil
	}
	newMessages, err := json.Marshal(messages)
	if err != nil {
		return nil, 0, fmt.Errorf("maskBody remarshal messages: %w", err)
	}
	top["messages"] = newMessages
	out, err := json.Marshal(top)
	if err != nil {
		return nil, 0, fmt.Errorf("maskBody remarshal: %w", err)
	}
	return out, total, nil
}

func maskContent(content json.RawMessage, f filter.RequestFilter) (json.RawMessage, int, error) {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		masked, n := f.Mask(text)
		if n == 0 {
			return content, 0, nil
		}
		body, err := json.Marshal(masked)
		return body, n, err
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, 0, fmt.Errorf("maskContent: %w", err)
	}
	total := 0
	for i, blockRaw := range blocks {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(blockRaw, &block); err != nil {
			return nil, 0, fmt.Errorf("maskContent block[%d]: %w", i, err)
		}
		var typ string
		_ = json.Unmarshal(block["type"], &typ)
		if typ != "text" {
			continue
		}
		var text string
		if err := json.Unmarshal(block["text"], &text); err != nil {
			continue
		}
		masked, n := f.Mask(text)
		if n == 0 {
			continue
		}
		maskedBody, err := json.Marshal(masked)
		if err != nil {
			return nil, 0, err
		}
		block["text"] = maskedBody
		remarshaled, err := json.Marshal(block)
		if err != nil {
			return nil, 0, err
		}
		blocks[i] = remarshaled
		total += n
	}
	if total == 0 {
		return content, 0, nil
	}
	out, err := json.Marshal(blocks)
	return out, total, err
}

// pricedTargets projects the resolved chain onto the (provider, upstream) pairs
// the request could be billed against — the same key pricing.CostUSDMicros
// uses — so the pricing guard checks exactly what settlement will look up.
func pricedTargets(chain []router.ChainTarget) []governance.PricedTarget {
	out := make([]governance.PricedTarget, 0, len(chain))
	for _, ct := range chain {
		out = append(out, governance.PricedTarget{Provider: ct.ProviderName, Upstream: ct.Upstream})
	}
	return out
}
