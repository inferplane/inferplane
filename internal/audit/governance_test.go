package audit

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Only the gateway denial record carries the limit that refused it.
func TestGovernanceOnlyOnDenialRecord(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	ref := &GovernanceRef{Scope: GovScopeTeam, Limit: GovLimitRPM, Source: GovSourcePolicy,
		Policy: &PolicyRuleRef{Name: "team-rate", Generation: 3, Rule: "rpm"}}
	SetGovernance(ctx, ref)
	ref.Policy.Rule = "mutated" // the collector keeps its own copy
	denied := Record{Event: "request_started", Outcome: &OutcomeRef{Status: 429}}
	Finish(ctx, &denied)
	done := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}}
	Finish(ctx, &done)
	admitted := Record{Event: "request_started"}
	Finish(ctx, &admitted)
	if denied.Governance == nil || denied.Governance.Policy == nil || denied.Governance.Policy.Rule != "rpm" {
		t.Fatalf("denial governance = %+v", denied.Governance)
	}
	if done.Governance != nil || admitted.Governance != nil {
		t.Fatalf("governance leaked: completed=%+v admitted=%+v", done.Governance, admitted.Governance)
	}
	SetGovernance(ctx, nil)
	again := Record{Event: "request_started", Outcome: &OutcomeRef{Status: 429}}
	Finish(ctx, &again)
	if again.Governance != nil {
		t.Fatalf("cleared governance written: %+v", again.Governance)
	}
}

// The field is appended last; a policy without a generation omits it.
func TestGovernanceWireShape(t *testing.T) {
	r := Record{SchemaVersion: 1, Event: "request_started", ID: "01J", TS: "2026-10-29T00:00:00Z", Instance: "i",
		Principal: PrincipalRef{KeyID: "ik", Team: "t"}, Request: RequestRef{Ingress: "anthropic", ModelRequested: "m"},
		Outcome:    &OutcomeRef{Status: 402, Error: DenyTeamBudgetExceeded.Ptr()},
		Governance: &GovernanceRef{Scope: GovScopeTeam, Limit: GovLimitBudgetDay, Source: GovSourcePolicy, Policy: &PolicyRuleRef{Name: "p", Rule: "r"}}}
	got, err := r.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"event":"request_started","id":"01J","ts":"2026-10-29T00:00:00Z","instance":"i","principal":{"key_id":"ik","team":"t"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"outcome":{"status":402,"fallback_used":false,"partial":false,"error":"team_budget_exceeded"},"trace_id":null,"prev_hash":"","governance":{"scope":"team","limit":"budget_day","source":"policy","policy":{"name":"p","rule":"r"}}}`
	if string(got) != want {
		t.Fatalf("wire shape:\n got %s\nwant %s", got, want)
	}
}

// A chain mixing records with and without the field still verifies.
func TestGovernanceMixedChainVerifies(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []Sink{NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	w.Append(Record{SchemaVersion: 1, Event: "request_started", ID: "01A", TS: "2026-10-29T00:00:00Z", Outcome: &OutcomeRef{Status: 429, Error: DenyKeyRateLimited.Ptr()}})
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	SetGovernance(ctx, &GovernanceRef{Scope: GovScopeKey, Limit: GovLimitRPM, Source: GovSourceKey})
	rec := Record{SchemaVersion: 1, Event: "request_started", ID: "01B", TS: "2026-10-29T00:00:01Z", Outcome: &OutcomeRef{Status: 429, Error: DenyKeyRateLimited.Ptr()}}
	Finish(ctx, &rec)
	w.Append(rec)
	w.Close()
	if !bytes.Contains(buf.Bytes(), []byte(`"governance":{"scope":"key","limit":"rpm","source":"key"}`)) {
		t.Fatalf("chain lacks the new field: %s", buf.Bytes())
	}
	res, err := Verify(bytes.NewReader(buf.Bytes()))
	if err != nil || !res.OK || res.Records != 2 {
		t.Fatalf("verify = %+v, %v", res, err)
	}
}
