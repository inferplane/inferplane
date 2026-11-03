package bedrockapi

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/filter"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
)

// claimsRole marks the protocol key "role" as protected text. Complete
// redaction never rewrites object keys, so it refuses every Converse request.
type claimsRole struct{}

func (claimsRole) Name() string { return "claims-role" }
func (claimsRole) Mask(text string) (string, int) {
	if text == "role" {
		return "x", 1
	}
	return text, 0
}

// A native Converse legacy redaction refusal writes one mask_failed denial
// with mask_failed routing evidence and keeps its 400; no provider is called.
func TestConverseLegacyMaskFailureWritesDenial(t *testing.T) {
	c := &nativeCapture{}
	hold := holderFor(map[string]providers.Provider{"p": c}, map[string]config.ModelConfig{
		"native": {Targets: []config.Target{{Provider: "p", Model: "native-id"}}},
	})
	w, buf := invokeWriter(t)
	h := NewConverseHandlerMetrics(router.New(hold), hold, w, nil, nil, false)
	h.SetMasking(&filter.Masking{Global: true, Filter: claimsRole{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowAll(invokeReq("native", `{"messages":[{"role":"user","content":[{"text":"hello"}]}]}`)))
	w.Close()
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "request could not be PII-masked") || c.last != nil {
		t.Fatalf("refusal changed: %d %s called=%v", rec.Code, rec.Body, c.last != nil)
	}
	var denials []audit.Record
	sc := bufio.NewScanner(buf)
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Event == "request_completed" {
			t.Fatalf("refused request completed: %s", sc.Bytes())
		}
		if r.Event == "request_started" && r.Outcome != nil {
			denials = append(denials, r)
		}
	}
	if len(denials) != 1 {
		t.Fatalf("denials %d", len(denials))
	}
	d := denials[0]
	if d.Outcome.Status != 400 || d.Outcome.Error == nil || *d.Outcome.Error != "mask_failed" || d.Request.Routing == nil || d.Request.Routing.Reason != "mask_failed" {
		t.Fatalf("denial = %+v routing = %+v", d.Outcome, d.Request.Routing)
	}
}
