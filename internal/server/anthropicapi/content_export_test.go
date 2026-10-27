package anthropicapi

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/filter"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
)

// Governed content export copies the EGRESS body — after masking — under the
// same record id as the audit record, with the subject used for the decision.
func TestMessagesContentExportUsesMaskedEgress(t *testing.T) {
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	h := NewMessagesHandlerWithAudit(recRouter(&recProvider{}), w)
	h.SetMasking(&filter.Masking{Filter: stubMasker{}, Global: true})
	var got []requestpolicy.ContentRecord
	h.SetContentExport(func(_ context.Context, c requestpolicy.ContentRecord) { got = append(got, c) })

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"call PII now"}]}`))
	req = req.WithContext(principal.With(req.Context(), keystore.Principal{Team: "acme", AllowedModels: []string{"*"}, KeyOptions: keystore.KeyOptions{Owner: "alice"}}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	w.Close()
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if len(got) != 1 {
		t.Fatalf("content exports = %d", len(got))
	}
	c := got[0]
	if !strings.Contains(string(c.Request), "call X now") || strings.Contains(string(c.Request), "PII") {
		t.Fatalf("exported request must be the masked egress body: %s", c.Request)
	}
	if len(c.Response) == 0 || c.Team != "acme" || c.Account != "alice" || c.Ingress != "anthropic" || c.Model != "m" || c.Upstream != "up" {
		t.Fatalf("content record = %+v", c)
	}
	if id := extractRecordID(t, buf.Bytes()); c.RecordID != id {
		t.Fatalf("content record id %q != audit record id %q", c.RecordID, id)
	}
}

func TestMessagesContentExportStreamHasNoResponse(t *testing.T) {
	h := NewMessagesHandler(testRouter())
	var got []requestpolicy.ContentRecord
	h.SetContentExport(func(_ context.Context, c requestpolicy.ContentRecord) { got = append(got, c) })
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, allowAll(httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-6","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))))
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	if len(got) != 1 || got[0].Response != nil || len(got[0].Request) == 0 {
		t.Fatalf("stream content exports = %+v", got)
	}
}

func extractRecordID(t *testing.T, raw []byte) string {
	t.Helper()
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if !bytes.Contains(line, []byte(`"event":"request_completed"`)) {
			continue
		}
		i := bytes.Index(line, []byte(`"id":"`))
		rest := line[i+len(`"id":"`):]
		return string(rest[:bytes.IndexByte(rest, '"')])
	}
	t.Fatalf("no request_completed record: %s", raw)
	return ""
}
