package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
	"github.com/inferplane/inferplane/pkg/schema"
)

func TestEvaluationFallbackCannotClaimLastAttemptCostAsKnownTotal(t *testing.T) {
	for _, attempts := range []int{1, 2} {
		data := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for range attempts {
				// No monetary authority in this fixture, but each invocation
				// traverses the same observation point as a real attempt.
				_, done, err := requestpolicy.ReserveBudget(r, nil, keystore.Principal{}, router.ChainTarget{}, nil, []byte(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				done()
			}
			in, out := int64(20), int64(3)
			requestpolicy.SettleBudget(r, &audit.CostRef{AmountUSDMicros: 23},
				&schema.Usage{InputTokens: &in, OutputTokens: &out}, true)
			io.WriteString(w, `{"output":{"message":{"content":[{"text":"ok"}]}},"stopReason":"end_turn"}`)
		})
		req := httptest.NewRequest("POST", "/admin/evaluations", strings.NewReader(`{"protocol":"bedrock-converse","model":"native","body":{"messages":[]}}`))
		req.Header.Set("X-Inferplane-Virtual-Key", "fixture")
		rec := httptest.NewRecorder()
		evaluationHandler(data).ServeHTTP(rec, req)
		var result evaluationResult
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.AccountingKnown != (attempts == 1) || result.Attempts != attempts ||
			result.UnsettledAttempts != attempts-1 || result.Cost.AmountUSDMicros != 23 {
			t.Fatalf("incorrect fallback accounting: %+v", result)
		}
	}
}

func TestEvaluationRequiresBothAdminAndVirtualKey(t *testing.T) {
	calls := 0
	data := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture-virtual" || r.Header.Get("X-Inferplane-Virtual-Key") != "" ||
			r.Header.Get("Cookie") != "" || r.URL.Path != "/model/native/converse" {
			t.Errorf("unexpected authority or path: %s", r.URL.Path)
		}
		io.WriteString(w, `{"output":{"message":{"content":[{"text":"hello"}]}},"stopReason":"end_turn"}`)
	})
	mux := WithEvaluations(http.NotFoundHandler(), data, []string{"fixture-admin"}, nil, adminauth.MappingConfig{}, nil)
	for _, tc := range []struct {
		admin, key string
		want       int
	}{{"", "fixture-virtual", 401}, {"fixture-admin", "", 400}, {"fixture-admin", "fixture-virtual", 200}} {
		req := httptest.NewRequest("POST", "/admin/evaluations", strings.NewReader(`{"protocol":"bedrock-converse","model":"native","body":{"messages":[]}}`))
		req.Header.Set("Authorization", "Bearer "+tc.admin)
		req.Header.Set("X-Inferplane-Virtual-Key", tc.key)
		req.Header.Set("Cookie", "admin-session=must-not-forward")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("unapproved evaluation reached data plane: %d", calls)
	}
}

func TestEvaluationDeniesMappedNonAdminBeforeDataPlane(t *testing.T) {
	calls := 0
	data := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ })
	verifier := &fakeVerifier{claims: adminauth.Claims{Subject: "fixture-member", Groups: []string{"members"}}}
	mapping := adminauth.MappingConfig{GroupMappings: []adminauth.GroupMapping{{Group: "members", Teams: []string{"fixture"}}}}
	mux := WithEvaluations(http.NotFoundHandler(), data, nil, verifier, mapping, nil)
	req := httptest.NewRequest("POST", "/admin/evaluations", strings.NewReader(`{"protocol":"bedrock-converse","model":"native","body":{"messages":[]}}`))
	req.Header.Set("Authorization", "Bearer "+jwtShaped)
	req.Header.Set("X-Inferplane-Virtual-Key", "fixture")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 || calls != 0 {
		t.Fatalf("member obtained admin evaluation access: status=%d calls=%d", rec.Code, calls)
	}
}

func TestEvaluationClassifiesResponseContracts(t *testing.T) {
	for _, tc := range []struct {
		protocol, raw, outcome string
	}{
		{"anthropic", `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`, "completed"},
		{"anthropic", `{"content":[],"stop_reason":"end_turn"}`, "empty_response"},
		{"anthropic", `{"content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens"}`, "truncated"},
		{"openai", `{"choices":[{"message":{"content":"","refusal":"denied"},"finish_reason":"stop"}]}`, "refused"},
		{"openai", `{"choices":[{"message":{"tool_calls":[{"id":"call"}]},"finish_reason":"tool_calls"}]}`, "completed"},
		{"responses", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal"}]}]}`, "refused"},
		{"responses", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`, "completed"},
		{"bedrock-converse", `{"__type":"UnknownOperationException"}`, "protocol_error"},
	} {
		_, _, got := evaluationOutput(tc.protocol, []byte(tc.raw))
		if got != tc.outcome {
			t.Fatalf("%s outcome=%s want=%s", tc.raw, got, tc.outcome)
		}
	}
}
func TestEvaluationObservesSettlementAndDoesNotCallRefusalSuccess(t *testing.T) {
	data := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in, out := int64(20), int64(3)
		requestpolicy.SettleBudget(r, &audit.CostRef{AmountUSDMicros: 12345678901234567},
			&schema.Usage{InputTokens: &in, OutputTokens: &out, AccountingUncertain: true}, true)
		io.WriteString(w, `{"output":{"message":{"content":[]}},"stopReason":"guardrail_intervened"}`)
	})
	req := httptest.NewRequest("POST", "/admin/evaluations", strings.NewReader(`{"protocol":"bedrock-converse","model":"native","body":{"messages":[]}}`))
	req.Header.Set("X-Inferplane-Virtual-Key", "fixture-virtual")
	rec := httptest.NewRecorder()
	evaluationHandler(data).ServeHTTP(rec, req)
	for _, want := range []string{`"http_status":200`, `"response_success":false`, `"stop_reason":"guardrail_intervened"`,
		`"amount_usd_micros":12345678901234567`, `"accounting_uncertain":true`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, rec.Body.String())
		}
	}
}

func TestEvaluationCannotChooseURLOrStreamOrChangeModel(t *testing.T) {
	calls := 0
	h := evaluationHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	for _, body := range []string{
		`{"protocol":"http://metadata.internal","model":"m","body":{}}`,
		`{"protocol":"anthropic","model":"m","body":{"model":"m","stream":true}}`,
		`{"protocol":"responses","model":"m","body":{"model":"other"}}`,
		`{"protocol":"bedrock-converse","model":"","body":{}}`,
		`{"protocol":"bedrock-converse","model":"m","body":null}`,
		`{"protocol":"bedrock-converse","model":"m","body":{},"url":"https://other.invalid"}`,
	} {
		req := httptest.NewRequest("POST", "/admin/evaluations", strings.NewReader(body))
		req.Header.Set("X-Inferplane-Virtual-Key", "fixture")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("unsafe evaluation accepted: %d %s", rec.Code, body)
		}
	}
	if calls != 0 {
		t.Fatal("invalid comparison made an inference call")
	}
}
