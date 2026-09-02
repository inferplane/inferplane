package anthropicapi

// Strategy P1 "undisclosed request mutation": when a provider drops request
// params the upstream rejects (ProxyRequest.ParamsStripped), the ingress
// must say so — x-inferplane-params-stripped on the response and
// params_stripped in the audit record. The plumbing is identical in the
// openai and bedrock ingresses (same fence location, same context stamp).

import (
	"bytes"
	"context"
	"iter"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

// strippingProvider mimics bedrock's strip tables: it drops params from the
// request and reports them on pr.ParamsStripped, then answers normally.
type strippingProvider struct{}

func (strippingProvider) Name() string               { return "stripping" }
func (strippingProvider) Models() []schema.ModelInfo { return nil }
func (strippingProvider) Complete(_ context.Context, pr *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	pr.ParamsStripped = append(pr.ParamsStripped, "temperature", "topP")
	resp := &schema.ChatResponse{ID: "msg_s", Type: "message", Role: "assistant", Model: "m", Content: []schema.ContentBlock{}}
	return &providers.ProxyResponse{StatusCode: 200, RawBody: []byte(`{"id":"msg_s","type":"message","role":"assistant","model":"m","content":[]}`), Parsed: resp}, nil
}
func (strippingProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, nil
}

func TestMessagesDisclosesStrippedParams(t *testing.T) {
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	provs := map[string]providers.Provider{"p": strippingProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}}
	h := NewMessagesHandlerWithAudit(router.New(holderFor(provs, models)), w)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","temperature":0,"messages":[]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	w.Close()

	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("x-inferplane-params-stripped"); got != "temperature,topP" {
		t.Fatalf("x-inferplane-params-stripped = %q, want %q", got, "temperature,topP")
	}
	if !strings.Contains(buf.String(), `"params_stripped":["temperature","topP"]`) {
		t.Fatalf("audit record missing params_stripped: %s", buf.String())
	}
}

func TestMessagesNoStripNoDisclosure(t *testing.T) {
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	provs := map[string]providers.Provider{"p": headerProvider{}}
	models := map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}}
	h := NewMessagesHandlerWithAudit(router.New(holderFor(provs, models)), w)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[]}`))
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	w.Close()

	if got := rec.Header().Get("x-inferplane-params-stripped"); got != "" {
		t.Fatalf("header must be absent when nothing was stripped, got %q", got)
	}
	if strings.Contains(buf.String(), "params_stripped") {
		t.Fatalf("audit must omit params_stripped when nothing was stripped: %s", buf.String())
	}
}
