package responsesapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/filter"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
)

type fixedRedactor struct {
	out []byte
	err error
}

func (f fixedRedactor) Redact(context.Context, string, []byte) ([]byte, error) { return f.out, f.err }

// An unreadable masked body is refused like a redaction error.
func TestLegacyMaskRefusesUnreadableResult(t *testing.T) {
	raw := []byte(`{"model":"premium","input":"hello"}`)
	if _, _, err := legacyMask(context.Background(), fixedRedactor{out: []byte(`{"model":`)}, raw); err == nil {
		t.Fatal("an unreadable masked body was accepted")
	}
	if _, _, err := legacyMask(context.Background(), fixedRedactor{err: errors.New("redaction refused")}, raw); err == nil {
		t.Fatal("a redaction error was accepted")
	}
	body, parsed, err := legacyMask(context.Background(), fixedRedactor{out: []byte(`{"model":"premium","input":"[x]"}`)}, raw)
	if err != nil || parsed == nil || parsed.Model != "premium" || string(body) != `{"model":"premium","input":"[x]"}` {
		t.Fatalf("masked body = %s parsed = %+v err = %v", body, parsed, err)
	}
}

// breakArgs rewrites encoded function arguments into text the redactor's
// reinspection cannot read, so legacy masking refuses the request.
type breakArgs struct{}

func (breakArgs) Name() string { return "break-args" }
func (breakArgs) Mask(text string) (string, int) {
	if strings.Contains(text, `{"k":`) {
		return "MASKED", 1
	}
	return text, 0
}

// A legacy mask refusal writes one terminal mask_failed denial and reaches no
// provider.
func TestResponsesLegacyMaskFailureWritesDenial(t *testing.T) {
	f := newStreamFixture(t)
	f.h.SetMasking(&filter.Masking{Global: true, Filter: breakArgs{}})
	body := `{"model":"premium","input":[{"type":"function_call","call_id":"c1","name":"f","arguments":"{\"k\":1}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req = req.WithContext(principal.With(evidenceCtx(), keystore.Principal{Team: "team", KeyID: "opaque-key", AllowedModels: []string{"*"}}))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	f.close(t)
	var denials []audit.Record
	sc := bufio.NewScanner(&f.audit)
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Event == "request_started" && r.Outcome != nil {
			denials = append(denials, r)
		}
	}
	if rec.Code != 403 || len(denials) != 1 || f.first.attempts+f.fallback.attempts != 0 {
		t.Fatalf("status %d denials %d attempts %d", rec.Code, len(denials), f.first.attempts+f.fallback.attempts)
	}
	d := denials[0]
	if d.Outcome.Error == nil || *d.Outcome.Error != "mask_failed" || d.Request.Routing == nil || d.Request.Routing.Reason != "mask_failed" {
		t.Fatalf("denial = %+v routing = %+v", d.Outcome, d.Request.Routing)
	}
}
