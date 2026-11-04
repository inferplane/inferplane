package server

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

// heldProvider blocks Complete until released, holding its request's budget.
type heldProvider struct {
	providers.Provider
	entered, release chan struct{}
}

func (p *heldProvider) Complete(ctx context.Context, r *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	p.entered <- struct{}{}
	<-p.release
	return p.Provider.Complete(ctx, r)
}

func inflightDo(mux http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("x-api-key", "dev-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestInflightBudgetRefusesGenerationAndKeepsCountsLocal(t *testing.T) {
	held := &heldProvider{Provider: mockprovider.New("m"), entered: make(chan struct{}), release: make(chan struct{})}
	holder := newHolder(map[string]providers.Provider{"p": held}, map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	m := metrics.New()
	mux := DataMux(router.New(holder), holder, store, nil, nil, m, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0,
		WithMaxRequestBytes(4096), WithMaxInflightRequestBytes(4096))
	body := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"` + strings.Repeat("x", 3000) + `"}]}`

	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- inflightDo(mux, "/v1/messages", body) }()
	<-held.entered // the first request now holds its bytes

	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", "/model/m/invoke", "/model/m/converse-stream"} {
		rec := inflightDo(mux, path, body)
		var reply struct {
			Type    string                               `json:"type"`
			Message string                               `json:"message"`
			Error   struct{ Type, Code, Message string } `json:"error"`
		}
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "2" || json.Unmarshal(rec.Body.Bytes(), &reply) != nil {
			t.Fatalf("%s over budget = %d %q %s, want 503 + Retry-After", path, rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
		}
		switch {
		case strings.HasPrefix(path, "/model/"):
			if rec.Header().Get("X-Amzn-ErrorType") != "ServiceUnavailableException" || reply.Message == "" {
				t.Fatalf("%s: not AWS-shaped: %s", path, rec.Body.String())
			}
		case path == "/v1/messages":
			if reply.Type != "error" || reply.Error.Type != "overloaded_error" {
				t.Fatalf("%s: not Anthropic-shaped: %s", path, rec.Body.String())
			}
		default:
			if reply.Error.Type != "server_error" || reply.Error.Code != "gateway_overloaded" {
				t.Fatalf("%s: not OpenAI-shaped: %s", path, rec.Body.String())
			}
		}
	}
	for path, field := range map[string]string{"/v1/messages/count_tokens": "input_tokens", "/model/m/count-tokens": "inputTokens"} {
		rec := inflightDo(mux, path, body)
		var out map[string]int64
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out[field] != int64(len(body)/4) {
			t.Fatalf("%s over budget = %d %s, want a local HTTP-200 estimate", path, rec.Code, rec.Body.String())
		}
	}
	refusals := map[string]float64{}
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "inferplane_inflight_refusals_total" {
			for _, metric := range family.GetMetric() {
				refusals[metric.GetLabel()[0].GetValue()] = metric.GetCounter().GetValue()
			}
		}
	}
	if refusals["generation"] != 5 || refusals["count"] != 2 {
		t.Fatalf("refusal metrics = %v, want 5 generation and 2 count", refusals)
	}

	close(held.release)
	if rec := <-first; rec.Code != http.StatusOK {
		t.Fatalf("held request = %d %s", rec.Code, rec.Body.String())
	}
	go func() {
		for range held.entered {
		}
	}()
	if rec := inflightDo(mux, "/v1/messages", body); rec.Code != http.StatusOK {
		t.Fatalf("budget not released: %d %s", rec.Code, rec.Body.String())
	}
	close(held.entered)
}

func TestInflightBudgetOffByDefault(t *testing.T) {
	held := &heldProvider{Provider: mockprovider.New("m"), entered: make(chan struct{}, 2), release: make(chan struct{})}
	holder := newHolder(map[string]providers.Provider{"p": held}, map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0, WithMaxRequestBytes(4096))
	body := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"` + strings.Repeat("x", 3000) + `"}]}`
	done := make(chan int, 2)
	for range 2 {
		go func() { done <- inflightDo(mux, "/v1/messages", body).Code }()
	}
	<-held.entered
	<-held.entered // both requests are in flight together
	close(held.release)
	if a, b := <-done, <-done; a != http.StatusOK || b != http.StatusOK {
		t.Fatalf("unset budget refused concurrent requests: %d %d", a, b)
	}
}

// heldStream yields the first event, then blocks until released.
type heldStream struct {
	providers.Provider
	entered, release chan struct{}
}

func (p *heldStream) Stream(ctx context.Context, r *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	inner, err := p.Provider.Stream(ctx, r)
	if err != nil {
		return nil, err
	}
	return func(yield func(*providers.StreamEvent, error) bool) {
		first := true
		for ev, err := range inner {
			if !yield(ev, err) {
				return
			}
			if first {
				first = false
				p.entered <- struct{}{}
				<-p.release
			}
		}
	}, nil
}

// A stream keeps its body (for fallback) until it ends, so it holds its bytes
// for the whole stream; a request without Content-Length reserves the whole
// per-request cap even when its body is small.
func TestInflightBudgetHeldByStreamsAndUndeclaredLengths(t *testing.T) {
	held := &heldStream{Provider: mockprovider.New("m"), entered: make(chan struct{}), release: make(chan struct{})}
	holder := newHolder(map[string]providers.Provider{"p": held}, map[string]config.ModelConfig{"m": {Targets: []config.Target{{Provider: "p", Model: "m"}}}})
	store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "ik_abc", Team: "t", AllowedModels: []string{"*"}}}
	mux := DataMux(router.New(holder), holder, store, nil, nil, nil, nil, nil, nil, nil, adminauth.MappingConfig{}, nil, 0,
		WithMaxRequestBytes(4096), WithMaxInflightRequestBytes(4096))
	small := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	stream := `{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"` + strings.Repeat("x", 3000) + `"}]}`

	done := make(chan int)
	go func() { done <- inflightDo(mux, "/v1/messages", stream).Code }()
	<-held.entered // first event written, stream still open
	if rec := inflightDo(mux, "/v1/messages", strings.Repeat(" ", 1200)+small); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("open stream released its budget early: %d", rec.Code)
	}
	close(held.release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("stream = %d", code)
	}

	undeclared := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(small))
		req.Header.Set("x-api-key", "dev-key")
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := undeclared(); code != http.StatusOK {
		t.Fatalf("undeclared length alone = %d, want admitted", code)
	}
	held.entered, held.release = make(chan struct{}), make(chan struct{})
	go func() {
		done <- inflightDo(mux, "/v1/messages", strings.Replace(stream, strings.Repeat("x", 3000), "x", 1)).Code
	}()
	<-held.entered // a small declared stream holds a few bytes
	if code := undeclared(); code != http.StatusServiceUnavailable {
		t.Fatalf("undeclared length beside another request = %d, want 503 (it reserves the 4096-byte cap)", code)
	}
	close(held.release)
	<-done
}

func inflightGauge(t *testing.T, m *metrics.Metrics) float64 {
	t.Helper()
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "inferplane_inflight_request_bytes" {
			return family.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatal("inferplane_inflight_request_bytes not registered")
	return 0
}

// A count declaring more than the per-request cap reserves at most the cap and,
// when it does not fit, is answered with the cap-bound estimate the count
// handlers themselves would give (never CL/4).
func TestInflightOversizedCountIsClamped(t *testing.T) {
	const limit = 4096
	b := &inflightBudget{limit: limit}
	var reserved int64
	h := inflightMiddleware(b, limit, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reserved = b.used
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader("{}"))
	req.ContentLength = 1 << 40
	h.ServeHTTP(httptest.NewRecorder(), req)
	if reserved != limit {
		t.Fatalf("oversized declared length reserved %d, want the %d cap", reserved, limit)
	}

	b.used = limit // budget full
	for path, field := range map[string]string{"/v1/messages/count_tokens": "input_tokens", "/model/m/count-tokens": "inputTokens"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.ContentLength = 1 << 40
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]int64
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out[field] != limit/4 {
			t.Fatalf("%s = %d %s, want 200 with %d", path, rec.Code, rec.Body.String(), limit/4)
		}
	}
}

func TestInflightGaugeReturnsToZero(t *testing.T) {
	m := metrics.New()
	b := &inflightBudget{limit: 1 << 30}
	h := inflightMiddleware(b, 1<<20, m, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("x"))
			req.ContentLength = int64(100 + i)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
	if g := inflightGauge(t, m); g != 0 {
		t.Fatalf("idle gauge = %v, want 0", g)
	}
}
