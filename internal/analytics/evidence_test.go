package analytics

import (
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
)

func evidencedRecord() audit.Record {
	return audit.Record{
		Event:   "request_completed",
		Outcome: &audit.OutcomeRef{Status: 200},
		Usage: &audit.UsageRef{Evidence: &audit.UsageEvidence{
			InputKnown: true, OutputKnown: true, InputTotalKnown: true, CacheWriteTTLKnown: true,
		}},
		Cost: &audit.CostRef{AmountUSDMicros: 100},
	}
}

func TestEvidenceClassification(t *testing.T) {
	errorText := "upstream_timeout"
	emptyError := ""
	tests := []struct {
		name    string
		change  func(*audit.Record)
		outcome OutcomeState
		cost    CostState
		partial bool
		code    string
	}{
		{"complete", func(*audit.Record) {}, "succeeded", "estimated", false, ""},
		{"nil outcome", func(r *audit.Record) { r.Outcome = nil }, "unknown", "estimated", false, ""},
		{"status below valid", func(r *audit.Record) { r.Outcome.Status = 199 }, "unknown", "estimated", false, ""},
		{"status above valid", func(r *audit.Record) { r.Outcome.Status = 600 }, "unknown", "estimated", false, ""},
		{"invalid status error", func(r *audit.Record) { r.Outcome.Status = 0; r.Outcome.Error = &errorText }, "unknown", "estimated", false, "upstream_timeout"},
		{"success upper boundary", func(r *audit.Record) { r.Outcome.Status = 299 }, "succeeded", "estimated", false, ""},
		{"failure lower boundary", func(r *audit.Record) { r.Outcome.Status = 300 }, "failed", "estimated", false, ""},
		{"failure upper boundary", func(r *audit.Record) { r.Outcome.Status = 599 }, "failed", "estimated", false, ""},
		{"failure with complete cost", func(r *audit.Record) { r.Outcome.Status = 400 }, "failed", "estimated", false, ""},
		{"error on success status", func(r *audit.Record) { r.Outcome.Error = &errorText }, "failed", "estimated", false, "upstream_timeout"},
		{"empty error", func(r *audit.Record) { r.Outcome.Error = &emptyError }, "succeeded", "estimated", false, ""},
		{"partial before invalid status", func(r *audit.Record) { r.Outcome.Partial = true; r.Outcome.Status = 0 }, "partial", "uncertain", true, ""},
		{"partial before failure", func(r *audit.Record) { r.Outcome.Partial = true; r.Outcome.Status = 500 }, "partial", "uncertain", true, ""},
		{"cost absent", func(r *audit.Record) { r.Cost = nil }, "succeeded", "unavailable", false, ""},
		{"partial cost absent", func(r *audit.Record) { r.Outcome.Partial = true; r.Cost = nil }, "partial", "unavailable", true, ""},
		{"missing pricing", func(r *audit.Record) { r.Cost.PricingMissing = true }, "succeeded", "unpriced", false, ""},
		{"missing pricing before missing evidence", func(r *audit.Record) { r.Cost.PricingMissing = true; r.Usage = nil }, "succeeded", "unpriced", false, ""},
		{"missing pricing before partial", func(r *audit.Record) { r.Cost.PricingMissing = true; r.Outcome.Partial = true }, "partial", "unpriced", true, ""},
		{"missing usage", func(r *audit.Record) { r.Usage = nil }, "succeeded", "legacy_unknown", false, ""},
		{"missing evidence", func(r *audit.Record) { r.Usage.Evidence = nil }, "succeeded", "legacy_unknown", false, ""},
		{"legacy before partial", func(r *audit.Record) { r.Usage.Evidence = nil; r.Outcome.Partial = true }, "partial", "legacy_unknown", true, ""},
		{"usage estimated", func(r *audit.Record) { r.Usage.Estimated = true }, "succeeded", "uncertain", false, ""},
		{"input unknown", func(r *audit.Record) { r.Usage.Evidence.InputKnown = false }, "succeeded", "uncertain", false, ""},
		{"output unknown", func(r *audit.Record) { r.Usage.Evidence.OutputKnown = false }, "succeeded", "uncertain", false, ""},
		{"input total unknown", func(r *audit.Record) { r.Usage.Evidence.InputTotalKnown = false }, "succeeded", "uncertain", false, ""},
		{"cache TTL unknown", func(r *audit.Record) { r.Usage.Evidence.CacheWriteTTLKnown = false }, "succeeded", "uncertain", false, ""},
		{"accounting uncertain", func(r *audit.Record) { r.Usage.Evidence.AccountingUncertain = true }, "succeeded", "uncertain", false, ""},
		{"negative amount", func(r *audit.Record) { r.Cost.AmountUSDMicros = -1 }, "succeeded", "uncertain", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := evidencedRecord()
			tc.change(&r)
			got := EvidenceOf(r)
			want := RecordEvidence{HasCost: r.Cost != nil, OutcomeState: tc.outcome, CostState: tc.cost, Partial: tc.partial, ErrorCode: tc.code}
			if got != want {
				t.Fatalf("EvidenceOf = %+v, want %+v", got, want)
			}
		})
	}
	for _, event := range []string{"", "request_started", "admin_key_created", "admin_denied", "body_accessed"} {
		if Observable(audit.Record{Event: event, Cost: &audit.CostRef{}}) {
			t.Fatalf("Observable(%q) = true", event)
		}
	}
	if !Observable(audit.Record{Event: "request_completed"}) {
		t.Fatal("costless completion must be observable")
	}
}

func TestUnknownErrorCodeIsBounded(t *testing.T) {
	for _, code := range []string{
		"context_length_exceeded", "invalid_encrypted_content", "request_canceled",
		"upstream_timeout", "stream_incomplete", "response_incompatible",
		"usage_missing", "client_write_failed", "upstream_error", "",
	} {
		if got := BoundedErrorCode(code); got != code {
			t.Errorf("BoundedErrorCode(%q) = %q", code, got)
		}
	}
	for _, code := range []string{"raw secret ik_do_not_expose", "timeout: upstream_timeout", " ", "UPSTREAM_ERROR"} {
		if got := BoundedErrorCode(code); got != "upstream_error" {
			t.Errorf("unknown code %q projected as %q", code, got)
		}
		r := evidencedRecord()
		r.Outcome.Error = &code
		if got := EvidenceOf(r); got.ErrorCode != "upstream_error" || got.OutcomeState != "failed" {
			t.Errorf("unknown error projection = %+v", got)
		}
	}
}

func TestZeroCostRequiresEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*audit.Record)
		want   CostState
	}{
		{"complete zero usage", func(*audit.Record) {}, "estimated"},
		{"nil cost", func(r *audit.Record) { r.Cost = nil }, "unavailable"},
		{"missing price", func(r *audit.Record) { r.Cost.PricingMissing = true }, "unpriced"},
		{"missing usage", func(r *audit.Record) { r.Usage = nil }, "legacy_unknown"},
		{"missing evidence", func(r *audit.Record) { r.Usage.Evidence = nil }, "legacy_unknown"},
		{"unknown input", func(r *audit.Record) { r.Usage.Evidence.InputKnown = false }, "uncertain"},
		{"partial", func(r *audit.Record) { r.Outcome.Partial = true }, "uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := evidencedRecord()
			r.Cost.AmountUSDMicros = 0
			tc.change(&r)
			if got := EvidenceOf(r).CostState; got != tc.want {
				t.Fatalf("zero cost state = %q, want %q", got, tc.want)
			}
		})
	}
}
