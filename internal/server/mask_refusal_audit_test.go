package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/filter"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/metrics"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/sensitivity"
	"github.com/inferplane/inferplane/internal/server/routingtest"
)

// maskDenials returns the terminal denials (request_started with an outcome)
// and fails if the refused request completed.
func maskDenials(t *testing.T, buf *bytes.Buffer) []audit.Record {
	t.Helper()
	var out []audit.Record
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Event == "request_completed" {
			t.Fatalf("refused request completed: %s", sc.Bytes())
		}
		if r.Event == "request_started" && r.Outcome != nil {
			out = append(out, r)
		}
	}
	return out
}

func spyCalls(f *routingtest.Fixture) int {
	return len(f.Public.Calls) + len(f.Private.Calls) + len(f.Retry.Calls)
}

// A legacy masking refusal on Chat and InvokeModel is a privacy denial: one
// mask_failed record with mask_failed routing evidence, the established 400
// and message, and no provider attempt. Messages cannot reach its refusal
// with a body it parsed, so anthropicapi pins that branch's helper.
func TestLegacyMaskRefusalIsAPrivacyDenial(t *testing.T) {
	const unmaskable = "request could not be PII-masked"
	const unsupported = "PII masking is enabled for your team but not supported on the OpenAI-compatible endpoint yet; use /v1/messages"
	for _, tc := range []struct{ name, path, body, message string }{
		{"openai/complete", "/v1/chat/completions", routingtest.Body("openai", "clean", false), unsupported},
		{"openai/stream", "/v1/chat/completions", routingtest.Body("openai", "clean", true), unsupported},
		// No messages array: the legacy walker has nothing it can mask.
		{"bedrock/complete/shape", "/model/premium/invoke", `{"inputText":"person@example.test"}`, unmaskable},
		{"bedrock/stream/shape", "/model/premium/invoke-with-response-stream", `{"inputText":"person@example.test"}`, unmaskable},
		// Masked text in a body the canonical request parser cannot read back.
		{"bedrock/complete/reparse", "/model/premium/invoke", `{"max_tokens":"16","messages":[{"role":"user","content":"person@example.test"}]}`, unmaskable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := routingtest.New(t, nil)
			aud, buf := policyAudit(t)
			store := stubStore{key: "dev-key", p: keystore.Principal{KeyID: "opaque-key", Team: "team", AllowedModels: []string{"*"}, KeyOptions: keystore.KeyOptions{Owner: "user"}}}
			m := metrics.New()
			h := DataMux(f.Router, f.Holder, store, aud, nil, m, &filter.Masking{Global: true, Filter: &policyMasker{}}, nil, nil, nil, adminauth.MappingConfig{}, nil, 0)
			rec := policyDo(h, tc.path, tc.body, true)
			aud.Close()
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), tc.message) {
				t.Fatalf("refusal changed: %d %s", rec.Code, rec.Body)
			}
			// Evidence only: no policy applied, so no routing decision is counted.
			families, err := m.Registry().Gather()
			if err != nil {
				t.Fatal(err)
			}
			for _, family := range families {
				if family.GetName() == "inferplane_routing_decisions_total" {
					t.Fatalf("refusal metered a routing decision: %v", family.Metric)
				}
			}
			denials := maskDenials(t, buf)
			if len(denials) != 1 || spyCalls(f) != 0 {
				t.Fatalf("denials %d provider calls %d", len(denials), spyCalls(f))
			}
			d := denials[0]
			if d.Outcome.Status != 400 || d.Outcome.Error == nil || *d.Outcome.Error != "mask_failed" || d.Request.Routing == nil || d.Request.Routing.Reason != "mask_failed" {
				t.Fatalf("denial = %+v routing = %+v", d.Outcome, d.Request.Routing)
			}
			if strings.Contains(buf.String(), "person@example.test") {
				t.Fatal("request text reached the audit record")
			}
		})
	}
}

type unreadableRedactor struct{ out string }

func (r unreadableRedactor) Redact(context.Context, string, []byte) ([]byte, error) {
	return []byte(r.out), nil
}

// A policy-masked body that cannot be read back is refused with one
// masking_failed denial before any provider attempt; status and message stay.
// Only injected components produce it: the shipped redactor keeps protocol
// structure and the router reinspects its result.
func TestUnreadablePolicyMaskIsAPrivacyDenial(t *testing.T) {
	for _, ing := range policyIngresses {
		for _, out := range []string{"", `{"model":`} {
			t.Run(ing.name()+"/"+map[bool]string{true: "empty", false: "unreadable"}[out == ""], func(t *testing.T) {
				f := routingtest.New(t, nil)
				f.Policies = []*policy.Policy{maskRoutingPolicy()}
				f.Router.SetRequestRedactor(unreadableRedactor{out: out})
				f.Router.SetRequestInspector(policyInspectorFunc(func(_ context.Context, _ string, raw []byte) (sensitivity.Result, error) {
					if bytes.Contains(raw, []byte("person@example.test")) {
						return sensitivity.Result{Complete: true, Categories: []string{"email"}}, nil
					}
					return sensitivity.Result{Complete: true}, nil
				}))
				aud, buf := policyAudit(t)
				raw := strings.Replace(routingtest.Body(ing.protocol, "clean", ing.stream), "hello", "person@example.test", 1)
				rec := policyDo(policyMux(f, aud, nil, nil, nil), ing.path, raw, true)
				aud.Close()
				if rec.Code != 400 || !strings.Contains(rec.Body.String(), "request could not be PII-masked") {
					t.Fatalf("refusal changed: %d %s", rec.Code, rec.Body)
				}
				denials := maskDenials(t, buf)
				if len(denials) != 1 || spyCalls(f) != 0 {
					t.Fatalf("denials %d provider calls %d", len(denials), spyCalls(f))
				}
				if d := denials[0]; d.Outcome.Status != 400 || d.Outcome.Error == nil || *d.Outcome.Error != "masking_failed" {
					t.Fatalf("denial = %+v", d.Outcome)
				}
				if strings.Contains(buf.String(), "person@example.test") {
					t.Fatal("request text reached the audit record")
				}
			})
		}
	}
}
