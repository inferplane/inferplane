package analytics

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inferplane/inferplane/internal/audit"
)

// Console kinds (ADR-057). Derived here, never written to the audit chain.
const (
	KindOK              = "ok"
	KindPolicyDenied    = "policy_denied"
	KindPrivacyBlocked  = "privacy_blocked"
	KindPrivacyMasked   = "privacy_masked"
	KindPrivacyInternal = "privacy_internal"
	KindUpstreamError   = "upstream_error"
	KindAutoModeBlocked = "auto_mode_blocked"
)

var AuditKinds = []string{KindOK, KindPolicyDenied, KindPrivacyBlocked, KindPrivacyMasked, KindPrivacyInternal, KindUpstreamError, KindAutoModeBlocked}

var (
	privacyBlockCodes = map[string]bool{"sensitive_blocked": true, "mask_failed": true, "masking_failed": true}
	policyDenyCodes   = map[string]bool{"model_forbidden": true, "no_safe_route": true, "budget_target_changed": true, "budget_authority_unavailable": true}
	closedCode        = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// AuditObservable selects terminal request records: completed records and
// started records that carry an outcome (every gateway denial).
func AuditObservable(r audit.Record) bool {
	switch r.Event {
	case "request_completed":
		return true
	case "request_started":
		return r.Outcome != nil
	}
	return false
}

func denyCodeOf(r audit.Record) string {
	if r.Outcome == nil {
		return ""
	}
	if r.Outcome.Error != nil {
		return *r.Outcome.Error
	}
	if r.Outcome.Status >= 400 && r.Request.Routing != nil {
		return r.Request.Routing.Reason
	}
	return ""
}

// maxAuditTextBytes matches the console API's free-text filter cap.
const maxAuditTextBytes = 256

// auditText makes a record-derived free-text value storable in both stores
// (Postgres TEXT rejects NUL and invalid UTF-8) and filterable: NUL bytes are
// dropped, invalid UTF-8 becomes U+FFFD, and the value is cut to
// maxAuditTextBytes on a rune boundary. Client-supplied model names reach
// denial records unvalidated, so every such column passes through here.
func auditText(s string) string {
	if len(s) <= maxAuditTextBytes && utf8.ValidString(s) && !strings.Contains(s, "\x00") {
		return s
	}
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
	if len(s) <= maxAuditTextBytes {
		return s
	}
	cut := maxAuditTextBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// LogAuditSkip reports a console row that a store refused (both modes). It
// names the record and a store code only: store messages can quote the
// offending request bytes.
func LogAuditSkip(id, code string) {
	fmt.Fprintf(os.Stderr, "inferplane: analytics audit console skipped record %q (%s)\n", id, code)
}

func boundedCode(s string) string {
	if s == "" || closedCode.MatchString(s) {
		return s
	}
	return "other"
}

// KindOf applies the spec's first-match-wins table. A completed record whose
// last attempt failed is an upstream error whatever its error code says (the
// code is the upstream's, and may collide with a gateway deny code); code
// matching applies to gateway decisions (started + outcome) and to legacy
// completed records that carry no attempts. Below 400, a partial stream and a
// committed stream whose last attempt is stream_failed (an upstream error
// event, or no message_stop, after the 200 was sent) are upstream errors.
func KindOf(r audit.Record) string {
	if r.AutoMode != nil && r.AutoMode.Verdict == "block" {
		return KindAutoModeBlocked
	}
	status, partial := 0, false
	if r.Outcome != nil {
		status, partial = r.Outcome.Status, r.Outcome.Partial
	}
	privacy, masked := "", r.Request.PIIMasked
	if rt := r.Request.Routing; rt != nil {
		privacy, masked = rt.Privacy, masked || rt.Masked
	}
	if status >= 400 {
		if r.Event == "request_completed" && len(r.Attempts) > 0 && r.Attempts[len(r.Attempts)-1].Status != audit.AttemptOK {
			return KindUpstreamError
		}
		code := denyCodeOf(r)
		switch {
		case privacy == "block" || privacyBlockCodes[code]:
			return KindPrivacyBlocked
		case audit.KnownDenyReason(code) || policyDenyCodes[code]:
			return KindPolicyDenied
		default:
			return KindUpstreamError
		}
	}
	streamFailed := r.Event == "request_completed" && len(r.Attempts) > 0 && r.Attempts[len(r.Attempts)-1].Status == audit.AttemptStreamFailed
	switch {
	case partial || streamFailed:
		return KindUpstreamError
	case masked:
		return KindPrivacyMasked
	case privacy == "internal_only":
		return KindPrivacyInternal
	}
	return KindOK
}

// AuditEvent is one console row; every string comes from a closed set or the
// record itself, and record free text is bounded by auditText.
type AuditEvent struct {
	ID              string   `json:"id"`
	TS              string   `json:"ts"`
	Event           string   `json:"event"`
	Kind            string   `json:"kind"`
	Team            string   `json:"team"`
	KeyID           string   `json:"key_id"`
	Ingress         string   `json:"ingress"`
	Stream          bool     `json:"stream"`
	Status          int      `json:"status"`
	DenyCode        string   `json:"deny_code,omitempty"`
	RoutingReason   string   `json:"routing_reason,omitempty"`
	Privacy         string   `json:"privacy,omitempty"`
	Masked          bool     `json:"masked,omitempty"`
	WouldRestrict   bool     `json:"would_restrict,omitempty"`
	PII             bool     `json:"pii,omitempty"`
	PolicyRef       string   `json:"policy_ref,omitempty"`
	PlannedBoundary string   `json:"planned_boundary,omitempty"`
	ActualBoundary  string   `json:"actual_boundary,omitempty"`
	RequestClass    string   `json:"request_class,omitempty"`
	SessionRef      string   `json:"session_ref,omitempty"`
	TraceID         string   `json:"trace_id,omitempty"`
	ModelRequested  string   `json:"model_requested"`
	ModelActual     string   `json:"model_actual,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Fallback        bool     `json:"fallback,omitempty"`
	AttemptCount    int      `json:"attempt_count"`
	AutoVerdict     string   `json:"auto_verdict,omitempty"`
	AutoStage       string   `json:"auto_stage,omitempty"`
	AutoSignature   string   `json:"auto_signature,omitempty"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CostMicros      int64    `json:"cost_micros"`
	TotalMs         int64    `json:"total_ms"`
	TTFTMs          int64    `json:"ttft_ms,omitempty"`
	HasBody         bool     `json:"has_body,omitempty"`
	Categories      []string `json:"categories,omitempty"`
}

// AuditEventRow adds the storage-only columns.
type AuditEventRow struct {
	AuditEvent
	TSMs       int64
	RecordJSON string
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// ProjectAuditEvent turns one terminal record into a console row. line is
// the exact audit line when known (Mode A); otherwise the canonical bytes.
func ProjectAuditEvent(r audit.Record, line []byte) (AuditEventRow, bool) {
	if !AuditObservable(r) || r.ID == "" {
		return AuditEventRow{}, false
	}
	row := AuditEventRow{AuditEvent: AuditEvent{ID: r.ID, TS: r.TS, Event: r.Event, Kind: KindOf(r),
		Team: r.Principal.Team, KeyID: r.Principal.KeyID, Ingress: r.Request.Ingress, Stream: r.Request.Stream,
		Masked: r.Request.PIIMasked, AttemptCount: len(r.Attempts), HasBody: r.BodyRef != nil && *r.BodyRef != ""}}
	if t, err := time.Parse(time.RFC3339Nano, r.TS); err == nil {
		row.TSMs = t.UnixMilli()
	}
	if r.Outcome != nil {
		row.Status, row.Fallback = r.Outcome.Status, r.Outcome.FallbackUsed
	}
	if row.Status >= 400 {
		row.DenyCode = boundedCode(denyCodeOf(r))
	}
	var cats []string
	rt := r.Request.Routing
	if rt != nil {
		row.RoutingReason, row.Privacy = boundedCode(rt.Reason), boundedCode(rt.Privacy)
		row.Masked = row.Masked || rt.Masked
		row.PlannedBoundary, row.ActualBoundary = auditText(rt.PlannedBoundary), auditText(rt.ActualBoundary)
		if len(rt.Policies) > 0 {
			p := rt.Policies[0]
			row.PolicyRef = auditText(fmt.Sprintf("%s#%d·%s", p.Name, p.Generation, p.Rule))
		}
		for _, c := range rt.Classifiers {
			row.WouldRestrict = row.WouldRestrict || c.WouldRestrict
		}
		for _, c := range rt.Categories {
			if closedCode.MatchString(c) && !slices.Contains(cats, c) {
				cats = append(cats, c)
			}
		}
	}
	slices.Sort(cats)
	row.Categories = cats
	row.PII = len(cats) > 0 || row.Masked || row.Privacy != "" || row.WouldRestrict
	if c := r.Client; c != nil {
		row.RequestClass, row.SessionRef = auditText(c.RequestClass), auditText(c.SessionRef)
	}
	if r.TraceID != nil {
		row.TraceID = auditText(*r.TraceID)
	}
	var reqModel, actModel, actProvider string
	if rt != nil {
		reqModel, actModel, actProvider = rt.RequestedModel, rt.ActualModel, rt.ActualProvider
	}
	row.ModelRequested = auditText(firstNonEmpty(reqModel, r.Request.ModelSubstitutedFrom, r.Request.ModelRequested))
	if r.Event == "request_completed" {
		row.ModelActual = auditText(firstNonEmpty(actModel, r.Request.ModelRequested))
	}
	provider := firstNonEmpty(r.Request.Provider, actProvider)
	if provider == "" && len(r.Attempts) > 0 {
		provider = r.Attempts[len(r.Attempts)-1].Provider
	}
	row.Provider = auditText(provider)
	row.Fallback = row.Fallback || len(r.Attempts) > 1
	if a := r.AutoMode; a != nil {
		row.AutoVerdict, row.AutoStage, row.AutoSignature = boundedCode(a.Verdict), boundedCode(a.Stage), auditText(a.Signature)
	}
	if u := r.Usage; u != nil {
		row.InputTokens, row.OutputTokens = u.InputTokens, u.OutputTokens
	}
	if c := r.Cost; c != nil {
		row.CostMicros = c.AmountUSDMicros
	}
	if l := r.Latency; l != nil {
		row.TotalMs, row.TTFTMs = l.TotalMs, l.TTFTMs
	}
	if len(line) > 0 {
		row.RecordJSON = string(bytes.TrimRight(line, "\r\n"))
	} else if b, err := r.Canonical(); err == nil {
		row.RecordJSON = string(b)
	}
	return row, true
}

// WaterfallRow is one bar of the trace timeline.
type WaterfallRow struct {
	Kind     string `json:"kind"` // gateway | attempt | settle | denied
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   string `json:"status,omitempty"`
	HTTP     int    `json:"http,omitempty"`
	StartMs  int64  `json:"start_ms"`
	DurMs    int64  `json:"dur_ms"`
}

type Waterfall struct {
	TotalMs int64          `json:"total_ms"`
	TTFTMs  int64          `json:"ttft_ms,omitempty"`
	Rows    []WaterfallRow `json:"rows"`
}

// WaterfallOf derives the timeline. A record with no latency has no rows:
// the console then says "no timing recorded". A gateway denial ends with a
// "denied" row (after any earlier attempts); a terminal completed record ends
// with "settle" when settlement ran after the last attempt.
func WaterfallOf(r audit.Record) Waterfall {
	w := Waterfall{Rows: []WaterfallRow{}}
	if r.Latency == nil {
		return w
	}
	w.TotalMs, w.TTFTMs = r.Latency.TotalMs, r.Latency.TTFTMs
	denied := r.Event == "request_started" && r.Outcome != nil
	if len(r.Attempts) == 0 {
		if denied {
			w.Rows = append(w.Rows, WaterfallRow{Kind: "denied", Status: boundedCode(denyCodeOf(r)), HTTP: r.Outcome.Status, DurMs: w.TotalMs})
		}
		return w
	}
	if first := r.Attempts[0].StartMs; first > 0 {
		w.Rows = append(w.Rows, WaterfallRow{Kind: "gateway", DurMs: first})
	}
	for _, a := range r.Attempts {
		w.Rows = append(w.Rows, WaterfallRow{Kind: "attempt", Provider: a.Provider, Model: a.Model, Status: a.Status, HTTP: a.HTTP, StartMs: a.StartMs, DurMs: a.DurMs})
	}
	last := r.Attempts[len(r.Attempts)-1]
	end := last.StartMs + last.DurMs
	if w.TotalMs <= end {
		w.TotalMs = end
	}
	switch {
	case denied:
		w.Rows = append(w.Rows, WaterfallRow{Kind: "denied", Status: boundedCode(denyCodeOf(r)), HTTP: r.Outcome.Status, StartMs: end, DurMs: w.TotalMs - end})
	case w.TotalMs > end:
		w.Rows = append(w.Rows, WaterfallRow{Kind: "settle", StartMs: end, DurMs: w.TotalMs - end})
	}
	return w
}
