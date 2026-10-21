package analytics

import "github.com/inferplane/inferplane/internal/audit"

type OutcomeState string
type CostState string

const (
	OutcomeSucceeded OutcomeState = "succeeded"
	OutcomeFailed    OutcomeState = "failed"
	OutcomePartial   OutcomeState = "partial"
	OutcomeUnknown   OutcomeState = "unknown"

	CostEstimated     CostState = "estimated"
	CostUncertain     CostState = "uncertain"
	CostUnpriced      CostState = "unpriced"
	CostUnavailable   CostState = "unavailable"
	CostLegacyUnknown CostState = "legacy_unknown"
)

// RecordEvidence describes observed completion and cost completeness. It is a
// derived read model and never authorizes settlement, refunds, or admission.
type RecordEvidence struct {
	HasCost      bool
	OutcomeState OutcomeState
	CostState    CostState
	Partial      bool
	ErrorCode    string
}

// Observable includes every completed request, whether or not it has cost.
func Observable(r audit.Record) bool { return r.Event == "request_completed" }

func EvidenceOf(r audit.Record) RecordEvidence {
	e := RecordEvidence{HasCost: r.Cost != nil, OutcomeState: OutcomeUnknown}
	if r.Outcome != nil {
		e.Partial = r.Outcome.Partial
		if r.Outcome.Error != nil {
			e.ErrorCode = BoundedErrorCode(*r.Outcome.Error)
		}
		switch {
		case e.Partial:
			e.OutcomeState = OutcomePartial
		case r.Outcome.Status < 200 || r.Outcome.Status > 599:
			// Invalid status is unknown even when an error is recorded.
		case e.ErrorCode != "" || r.Outcome.Status >= 300:
			e.OutcomeState = OutcomeFailed
		default:
			e.OutcomeState = OutcomeSucceeded
		}
	}
	switch {
	case r.Cost == nil:
		e.CostState = CostUnavailable
	case r.Cost.PricingMissing:
		e.CostState = CostUnpriced
	case r.Usage == nil || r.Usage.Evidence == nil:
		e.CostState = CostLegacyUnknown
	default:
		u := r.Usage.Evidence
		if e.Partial || r.Usage.Estimated || !u.InputKnown || !u.OutputKnown ||
			!u.InputTotalKnown || !u.CacheWriteTTLKnown || u.AccountingUncertain || r.Cost.AmountUSDMicros < 0 {
			e.CostState = CostUncertain
		} else {
			e.CostState = CostEstimated
		}
	}
	return e
}

// BoundedErrorCode keeps raw errors and client-controlled values out of the
// projection. Recognized provider codes and gateway causes are exact matches.
func BoundedErrorCode(code string) string {
	switch code {
	case "", "context_length_exceeded", "invalid_encrypted_content",
		"request_canceled", "upstream_timeout", "stream_incomplete",
		"response_incompatible", "usage_missing", "client_write_failed", "upstream_error":
		return code
	default:
		return "upstream_error"
	}
}

type OperationTotals struct {
	Completed                   int64 `json:"completed"`
	Succeeded                   int64 `json:"succeeded"`
	Failed                      int64 `json:"failed"`
	Partial                     int64 `json:"partial"`
	Unknown                     int64 `json:"unknown"`
	EstimatedRequests           int64 `json:"estimated_requests"`
	UncertainRequests           int64 `json:"uncertain_requests"`
	UnpricedRequests            int64 `json:"unpriced_requests"`
	UnavailableRequests         int64 `json:"unavailable_requests"`
	LegacyUnknownRequests       int64 `json:"legacy_unknown_requests"`
	EstimatedCostMicros         int64 `json:"estimated_cost_micros"`
	UncertainRecordedCostMicros int64 `json:"uncertain_recorded_cost_micros"`
	LegacyProjectionRecords     int64 `json:"legacy_projection_records"`
}
