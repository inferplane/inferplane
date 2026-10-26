package responsesapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/providers"
)

func TestNativeRecoveryFailureUsesSafeResponseFailedEvent(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "first-event", true: "committed"}[committed], func(t *testing.T) {
			f := newStreamFixture(t)
			f.first.name = "openai_responses"
			cause := errors.Join(responses.ErrFailed, &providers.UpstreamError{
				StatusCode: 400, Body: []byte(`{"error":{"code":"context_length_exceeded","message":"private"}}`),
			})
			if committed {
				f.first.steps = append(f.first.steps, startStep(nil))
			}
			f.first.steps = append(f.first.steps, streamStep{err: cause})
			rec := httptest.NewRecorder()
			f.serve(t, rec, context.Background())
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "event: response.failed\n") ||
				!strings.Contains(rec.Body.String(), `"status":"failed"`) ||
				!strings.Contains(rec.Body.String(), `"code":"context_length_exceeded"`) ||
				strings.Contains(rec.Body.String(), "private") || f.fallback.attempts != 0 {
				t.Fatalf("native recovery signal lost: status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestResponsesRateAdmissionIsNotBillingQuota(t *testing.T) {
	f := newStreamFixture(t)
	f.h.gov = governance.NewGovernor(map[string]governance.TeamPolicy{
		"team": {RatePerMin: 1, RateBurst: 1},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	sub := governance.Subject{Team: "team"}
	if decision := f.h.gov.PreCheck(sub, governance.KeyPolicy{}, 1); !decision.Allowed {
		t.Fatal("fixture failed to consume the first rate permit")
	} else if decision.Admission != nil {
		decision.Admission.Close()
	}
	rec := httptest.NewRecorder()
	f.serve(t, rec, context.Background())
	var payload struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 429 || payload.Error.Type != "rate_limit_exceeded" || f.first.attempts != 0 {
		t.Fatalf("rate refusal became billing quota: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
