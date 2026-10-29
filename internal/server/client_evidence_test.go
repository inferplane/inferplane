package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/clientctx"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

// The option must default to DefaultMode and wrap inside KeyAuth.
func TestDataMuxOptionsDefaultClientEvidence(t *testing.T) {
	o := newDataMuxOptions()
	if o.clientEvidence != clientctx.DefaultMode() {
		t.Fatalf("default = %+v", o.clientEvidence)
	}
	WithClientEvidence(clientctx.Mode{})(&o)
	if o.clientEvidence != (clientctx.Mode{}) {
		t.Fatalf("option not applied: %+v", o.clientEvidence)
	}
}

func TestEvidenceWrapperAttachesCollector(t *testing.T) {
	var ok bool
	h := clientctx.Middleware(clientctx.DefaultMode(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := audit.Record{Event: "request_completed", Outcome: &audit.OutcomeRef{Status: 200}}
		audit.Finish(r.Context(), &rec)
		ok = rec.Latency != nil
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil))
	if !ok {
		t.Fatal("no evidence collector attached")
	}
}

// ctxProbe records the client evidence its provider call sees.
type ctxProbe struct {
	providers.Provider
	client *audit.ClientRef
	seen   bool
}

func (p *ctxProbe) Complete(ctx context.Context, req *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	p.client = audit.ClientFrom(ctx)
	rec := audit.Record{Event: "request_completed", Outcome: &audit.OutcomeRef{Status: 200}}
	audit.Finish(ctx, &rec)
	p.seen = rec.Latency != nil
	return p.Provider.Complete(ctx, req)
}

// The real DataMux must attach the collector inside KeyAuth, so handlers see
// the key-scoped session digest and never the raw session ID.
func TestDataMuxAttachesClientEvidence(t *testing.T) {
	probe := &ctxProbe{Provider: mockprovider.New("claude-sonnet-4-6")}
	provs := map[string]providers.Provider{"p": probe}
	models := map[string]config.ModelConfig{
		"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "p", Model: "claude-sonnet-4-6"}}},
	}
	holder := newHolder(provs, models)
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)

	body := `{"model":"claude-sonnet-4-6","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-api-key", "dev-key")
	req.Header.Set("X-Claude-Code-Session-Id", "sess-1")
	req.Header.Set("X-Claude-Code-Request-Class", "main")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if !probe.seen {
		t.Fatal("provider call saw no evidence collector")
	}
	if probe.client == nil || probe.client.RequestClass != "main" || probe.client.SessionRef != clientctx.SessionRef("ik_abc", "sess-1") {
		t.Fatalf("client = %+v", probe.client)
	}
}
