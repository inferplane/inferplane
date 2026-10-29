package responsesapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

func evidenceCtx() context.Context {
	return audit.WithEvidence(context.Background(), time.Now(), &audit.ClientRef{RequestClass: "main"})
}

func TestResponsesFallbackRowsCarryCumulativeAttempts(t *testing.T) {
	f := newStreamFixture(t)
	f.first.openErr = &providers.UpstreamError{StatusCode: 503, Body: []byte(`{"error":{"code":"overloaded"}}`)}
	records := f.serve(t, httptest.NewRecorder(), evidenceCtx())
	if len(records) != 2 {
		t.Fatalf("want one completed record per attempt, got %d", len(records))
	}
	first, second := records[0], records[1]
	if first.Outcome.Status != 503 || len(first.Attempts) != 1 || first.Attempts[0].Status != audit.AttemptUpstreamError || first.Attempts[0].HTTP != 503 {
		t.Fatalf("first row = %+v", first)
	}
	if len(second.Attempts) != 2 || second.Attempts[1].Status != audit.AttemptOK || !second.Outcome.FallbackUsed || second.Client == nil {
		t.Fatalf("second row = %+v", second)
	}
}

func TestResponsesNonStreamFallbackRowsCarryCumulativeAttempts(t *testing.T) {
	f := newStreamFixture(t)
	f.first.complete = func() (*providers.ProxyResponse, error) {
		return &providers.ProxyResponse{StatusCode: 429, RawBody: []byte(`{"error":{"code":"rate_limit"}}`)}, nil
	}
	in, out := int64(10), int64(2)
	f.fallback.complete = func() (*providers.ProxyResponse, error) {
		return &providers.ProxyResponse{StatusCode: 200, RawBody: []byte(`{"id":"r"}`),
			Parsed: &schema.ChatResponse{ID: "r", Role: "assistant", Usage: &schema.Usage{InputTokens: &in, OutputTokens: &out}}}, nil
	}
	rec := httptest.NewRecorder()
	records := f.serveBody(t, rec, evidenceCtx(), `{"model":"premium","input":"hello"}`)
	if len(records) != 2 {
		t.Fatalf("records=%d body=%s", len(records), rec.Body.String())
	}
	if a := records[0].Attempts; len(a) != 1 || a[0].Status != audit.AttemptUpstreamError || a[0].HTTP != 429 {
		t.Fatalf("first attempts = %+v", a)
	}
	if a := records[1].Attempts; len(a) != 2 || a[1].Status != audit.AttemptOK || a[1].HTTP != 200 {
		t.Fatalf("second attempts = %+v", a)
	}
}

func TestResponsesStreamFailedAfterFirstOutput(t *testing.T) {
	f := newStreamFixture(t)
	f.first.steps = []streamStep{startStep(observedStreamUsage()), {err: &providers.UpstreamError{StatusCode: 500}}}
	records := f.serve(t, httptest.NewRecorder(), evidenceCtx())
	if len(records) != 1 || len(records[0].Attempts) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if a := records[0].Attempts[0]; a.Status != audit.AttemptStreamFailed || a.HTTP != 200 {
		t.Fatalf("attempt = %+v", a)
	}
}

func TestResponsesCanceledContextRecordsCanceled(t *testing.T) {
	f := newStreamFixture(t)
	ctx, cancel := context.WithCancel(evidenceCtx())
	f.first.onOpen = cancel
	f.first.openErr = context.Canceled
	records := f.serve(t, httptest.NewRecorder(), ctx)
	if len(records) != 1 || len(records[0].Attempts) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if a := records[0].Attempts[0]; a.Status != audit.AttemptCanceled || a.HTTP != 0 {
		t.Fatalf("attempt = %+v", a)
	}
	if f.fallback.attempts != 0 {
		t.Fatal("canceled request fell back")
	}
}

func slowSettleCtx() context.Context {
	return requestpolicy.WithSettlementObserver(evidenceCtx(), func(*audit.CostRef, *schema.Usage, bool) {
		time.Sleep(20 * time.Millisecond)
	})
}

func requireAttemptEndedBeforeSettle(t *testing.T, done audit.Record) {
	t.Helper()
	if len(done.Attempts) == 0 || done.Latency == nil {
		t.Fatalf("record = %+v", done)
	}
	a := done.Attempts[len(done.Attempts)-1]
	if a.Status != audit.AttemptOK || a.HTTP != 200 {
		t.Fatalf("attempt = %+v", a)
	}
	if gap := done.Latency.TotalMs - (a.StartMs + a.DurMs); gap < 15 {
		t.Fatalf("attempt %+v includes settlement: total %d, gap %d ms (want >= 15)", a, done.Latency.TotalMs, gap)
	}
}

func TestResponsesStreamAttemptEndsBeforeSettlement(t *testing.T) {
	f := newStreamFixture(t)
	f.first.steps = []streamStep{startStep(observedStreamUsage()), stopStep()}
	records := f.serve(t, httptest.NewRecorder(), slowSettleCtx())
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	requireAttemptEndedBeforeSettle(t, records[0])
}

func TestResponsesCompleteAttemptEndsBeforeSettlement(t *testing.T) {
	f := newStreamFixture(t)
	in, out := int64(10), int64(2)
	f.first.complete = func() (*providers.ProxyResponse, error) {
		return &providers.ProxyResponse{StatusCode: 200, RawBody: []byte(`{"id":"r"}`),
			Parsed: &schema.ChatResponse{ID: "r", Role: "assistant", Usage: &schema.Usage{InputTokens: &in, OutputTokens: &out}}}, nil
	}
	records := f.serveBody(t, httptest.NewRecorder(), slowSettleCtx(), `{"model":"premium","input":"hello"}`)
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	requireAttemptEndedBeforeSettle(t, records[0])
}

func TestResponsesDenialCarriesClient(t *testing.T) {
	f := newStreamFixture(t)
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"premium","input":"hello"}`))
	p := keystore.Principal{Team: "team", KeyID: "k", AllowedModels: []string{"other"}}
	ctx := audit.WithEvidence(principal.With(req.Context(), p), time.Now(), &audit.ClientRef{RequestClass: "main"})
	f.h.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))
	f.close(t)
	var denied *audit.Record
	sc := bufio.NewScanner(&f.audit)
	for sc.Scan() {
		var r audit.Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Event == "request_started" && r.Outcome != nil {
			denied = &r
		}
	}
	if denied == nil || denied.Client == nil || denied.Latency == nil {
		t.Fatalf("denied record = %+v", denied)
	}
}
