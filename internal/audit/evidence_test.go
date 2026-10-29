package audit

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A record without the new fields must keep its historical bytes.
func TestRecordWithoutEvidenceKeepsBytes(t *testing.T) {
	r := Record{SchemaVersion: 1, Event: "request_completed", ID: "01J", TS: "2026-10-29T00:00:00Z", Instance: "i",
		Principal: PrincipalRef{KeyID: "ik", Team: "t"}, Request: RequestRef{Ingress: "anthropic", ModelRequested: "m"},
		Outcome: &OutcomeRef{Status: 200}}
	got, err := r.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"event":"request_completed","id":"01J","ts":"2026-10-29T00:00:00Z","instance":"i","principal":{"key_id":"ik","team":"t"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"outcome":{"status":200,"fallback_used":false,"partial":false,"error":null},"trace_id":null,"prev_hash":""}`
	if string(got) != want {
		t.Fatalf("legacy bytes changed:\n got %s\nwant %s", got, want)
	}
}

func TestFinishStampsCompletedRecord(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), &ClientRef{RequestClass: "main", SessionRef: "s1:00"})
	endA := BeginAttempt(ctx, "bad", "m1")
	NoteAttemptHTTP(ctx, 503)
	endA(AttemptUpstreamError, 0)
	BeginAttempt(ctx, "good", "m2") // left open: Finish closes it from the outcome
	MarkFirstToken(ctx)
	rec := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}}
	Finish(ctx, &rec)
	if len(rec.Attempts) != 2 {
		t.Fatalf("attempts = %+v", rec.Attempts)
	}
	if a := rec.Attempts[0]; a.Provider != "bad" || a.Status != AttemptUpstreamError || a.HTTP != 503 {
		t.Fatalf("first attempt = %+v", a)
	}
	if a := rec.Attempts[1]; a.Provider != "good" || a.Status != AttemptOK || a.HTTP != 200 {
		t.Fatalf("second attempt = %+v", a)
	}
	if !rec.Outcome.FallbackUsed || strings.Join(rec.Outcome.FallbackChain, ",") != "bad,good" {
		t.Fatalf("fallback fields = %+v", rec.Outcome)
	}
	if rec.Latency == nil || rec.Latency.TTFTMs < 1 {
		t.Fatalf("latency = %+v", rec.Latency)
	}
	if rec.Client == nil || rec.Client.RequestClass != "main" {
		t.Fatalf("client = %+v", rec.Client)
	}
}

func TestFinishDeniedStartedRecord(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), &ClientRef{RequestClass: "subagent"})
	rec := Record{Event: "request_started", Outcome: &OutcomeRef{Status: 403}}
	Finish(ctx, &rec)
	if rec.Client == nil || rec.Latency == nil || rec.Attempts != nil || rec.AutoMode != nil {
		t.Fatalf("denied record = %+v", rec)
	}
}

func TestFinishLeavesAdmittedStartedRecord(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), &ClientRef{RequestClass: "main"})
	rec := Record{Event: "request_started"}
	Finish(ctx, &rec)
	if rec.Client != nil || rec.Latency != nil {
		t.Fatalf("admitted started record must stay untouched: %+v", rec)
	}
}

func TestFinishKeepsExistingLatency(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	rec := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}, Latency: &LatencyRef{TotalMs: 42}}
	Finish(ctx, &rec)
	if rec.Latency.TotalMs != 42 {
		t.Fatalf("latency overwritten: %+v", rec.Latency)
	}
}

func TestAttemptsAreBounded(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	for i := range 10 {
		end := BeginAttempt(ctx, "p"+string(rune('a'+i)), "m")
		if i < 9 {
			end(AttemptUpstreamError, 503)
		}
	}
	rec := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}}
	Finish(ctx, &rec)
	if len(rec.Attempts) != 8 || rec.Attempts[6].Provider != "pg" || rec.Attempts[7].Provider != "pj" {
		t.Fatalf("bounded attempts = %+v", rec.Attempts)
	}
}

func TestAutoModeOnlyOnCompleted(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	SetAutoMode(ctx, &AutoModeRef{Verdict: "block", Stage: "s1", Signature: "claude-code-2.1"})
	denied := Record{Event: "request_started", Outcome: &OutcomeRef{Status: 429}}
	Finish(ctx, &denied)
	done := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}}
	Finish(ctx, &done)
	if denied.AutoMode != nil || done.AutoMode == nil || done.AutoMode.Verdict != "block" {
		t.Fatalf("auto mode: denied=%+v done=%+v", denied.AutoMode, done.AutoMode)
	}
}

func TestNoEvidenceIsNoop(t *testing.T) {
	end := BeginAttempt(context.Background(), "p", "m")
	end(AttemptOK, 200)
	MarkFirstToken(context.Background())
	rec := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}}
	Finish(context.Background(), &rec)
	if rec.Latency != nil || rec.Attempts != nil {
		t.Fatalf("no collector must mean no change: %+v", rec)
	}
}

func TestMixedChainVerifies(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []Sink{NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	w.Append(Record{SchemaVersion: 1, Event: "request_completed", ID: "01A", TS: "2026-10-29T00:00:00Z", Outcome: &OutcomeRef{Status: 200}})
	ctx := WithEvidence(context.Background(), time.Now(), &ClientRef{RequestClass: "main"})
	rec := Record{SchemaVersion: 1, Event: "request_completed", ID: "01B", TS: "2026-10-29T00:00:01Z", Outcome: &OutcomeRef{Status: 200}}
	Finish(ctx, &rec)
	w.Append(rec)
	w.Close()
	res, err := Verify(bytes.NewReader(buf.Bytes()))
	if err != nil || !res.OK || res.Records != 2 {
		t.Fatalf("verify = %+v, %v", res, err)
	}
}

func TestKnownDenyReason(t *testing.T) {
	if !KnownDenyReason("team_budget_exceeded") || !KnownDenyReason("pricing_missing") || KnownDenyReason("upstream_error") {
		t.Fatal("deny reason set wrong")
	}
}

func TestOpenAttemptHTTP(t *testing.T) {
	if got := OpenAttemptHTTP(context.Background()); got != 0 {
		t.Fatalf("no collector: got %d, want 0", got)
	}
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	if got := OpenAttemptHTTP(ctx); got != 0 {
		t.Fatalf("no attempt: got %d, want 0", got)
	}
	end := BeginAttempt(ctx, "p", "m")
	if got := OpenAttemptHTTP(ctx); got != 0 {
		t.Fatalf("open attempt without note: got %d, want 0", got)
	}
	NoteAttemptHTTP(ctx, 503)
	if got := OpenAttemptHTTP(ctx); got != 503 {
		t.Fatalf("after note: got %d, want 503", got)
	}
	end(AttemptUpstreamError, 503)
	if got := OpenAttemptHTTP(ctx); got != 0 {
		t.Fatalf("after attempt ended: got %d, want 0", got)
	}
}
