package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

type contentSpy struct {
	mu   sync.Mutex
	recs []requestpolicy.ContentRecord
}

func (s *contentSpy) export(_ context.Context, c requestpolicy.ContentRecord) {
	s.mu.Lock()
	s.recs = append(s.recs, c)
	s.mu.Unlock()
}

// Every generation ingress must hand governed content export the same egress
// bytes body capture sees: one record per completed request, the request body
// always, the response only when it was buffered.
func TestEveryIngressExportsContent(t *testing.T) {
	for _, i := range authorityIngresses() {
		t.Run(i.name(), func(t *testing.T) {
			f := routingtest.New(t, nil)
			in, out := int64(2), int64(1)
			f.Public.Usage = &schema.Usage{InputTokens: &in, OutputTokens: &out}
			spy := &contentSpy{}
			rec := policyDo(policyMux(f, nil, nil, nil, nil, WithContentExport(spy.export)), i.path, authorityBody(i), true)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if len(spy.recs) != 1 {
				t.Fatalf("content exports = %d", len(spy.recs))
			}
			c := spy.recs[0]
			if c.Team != "team" || c.Account != "user" || c.RecordID == "" || len(c.Request) == 0 || c.Upstream == "" {
				t.Fatalf("content record = %+v", c)
			}
			if i.stream != (c.Response == nil) {
				t.Fatalf("stream=%v but response present=%v", i.stream, c.Response != nil)
			}
		})
	}
}

func TestConverseExportsContent(t *testing.T) {
	p := nativeMuxProvider{mockprovider.New("native")}
	holder := newHolder(map[string]providers.Provider{"p": p}, map[string]config.ModelConfig{
		"native": {Targets: []config.Target{{Provider: "p", Model: "native"}}},
	})
	store := stubStore{key: "fixture-key", p: keystore.Principal{Team: "team", AllowedModels: []string{"*"}}}
	spy := &contentSpy{}
	mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0, WithContentExport(spy.export))
	req := httptest.NewRequest(http.MethodPost, "/model/native/converse", strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"hello"}]}]}`))
	req.Header.Set("X-Api-Key", "fixture-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(spy.recs) != 1 || spy.recs[0].Ingress != "bedrock-converse" || len(spy.recs[0].Request) == 0 {
		t.Fatalf("converse content exports = %+v", spy.recs)
	}
}

// The privacy guarantee of content export: a request refused before egress
// (privacy Block, model access) exports nothing, on every ingress.
func TestDeniedRequestsExportNoContent(t *testing.T) {
	for _, i := range authorityIngresses() {
		t.Run(i.name()+"/privacy-block", func(t *testing.T) {
			f := routingtest.New(t, nil)
			f.Policies = []*policy.Policy{routingtest.Privacy(v1alpha1.Block)}
			spy := &contentSpy{}
			rec := policyDo(policyMux(f, nil, nil, nil, nil, WithContentExport(spy.export)), i.path, routingtest.Body(i.protocol, "system", i.stream), true)
			if rec.Code/100 != 4 || len(f.Public.Calls)+len(f.Private.Calls) != 0 {
				t.Fatalf("expected a refusal before egress: status=%d calls=%d", rec.Code, len(f.Public.Calls)+len(f.Private.Calls))
			}
			if len(spy.recs) != 0 {
				t.Fatalf("a blocked request exported content: %+v", spy.recs)
			}
		})
		t.Run(i.name()+"/model-access", func(t *testing.T) {
			f := routingtest.New(t, nil)
			spy := &contentSpy{}
			store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "opaque-key", Team: "team", AllowedModels: []string{"other-model"}, KeyOptions: keystore.KeyOptions{Owner: "user"}}}
			mux := DataMux(f.Router, f.Holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0, WithContentExport(spy.export))
			rec := policyDo(mux, i.path, authorityBody(i), true)
			if rec.Code/100 != 4 || len(f.Public.Calls) != 0 {
				t.Fatalf("expected a model-access refusal before egress: status=%d", rec.Code)
			}
			if len(spy.recs) != 0 {
				t.Fatalf("a denied request exported content: %+v", spy.recs)
			}
		})
	}
}
