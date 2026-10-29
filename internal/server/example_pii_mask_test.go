package server

import (
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/sensitivity"
	"github.com/inferplane/inferplane/internal/server/routingtest"
)

// The shipped email-masking example masks an email on every ingress and the
// audit record carries category email, masked and the example's rule (the
// audit console's PII row, spec success criterion 4).
func TestExamplePIIMaskPolicyMasksEmail(t *testing.T) {
	policies, _, err := policy.LoadPaths("../../examples/policies/pii-email-mask.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, ingress := range policyIngresses {
		t.Run(ingress.name(), func(t *testing.T) {
			f := routingtest.New(t, nil)
			f.Policies = policies
			f.Router.SetRequestRedactor(sensitivity.NewRedactor())
			body := `{"model":"premium","max_tokens":16,"messages":[{"role":"user","content":"contact alice@example.com"}]}`
			if ingress.stream {
				body = strings.TrimSuffix(body, "}") + `,"stream":true}`
			}
			aud, buf := policyAudit(t)
			rec := policyDo(policyMux(f, aud, nil, nil, nil), ingress.path, body, true)
			aud.Close()
			if rec.Code != 200 || len(f.Public.Calls) != 1 || strings.Contains(f.Public.Calls[0].Body, "alice@example.com") {
				t.Fatalf("status %d calls %d", rec.Code, len(f.Public.Calls))
			}
			for _, want := range []string{`"categories":["email"]`, `"masked":true`, `"policies":[{"name":"pii-email-mask","generation":1,"rule":"mask-detected"}]`} {
				if !strings.Contains(buf.String(), want) {
					t.Fatalf("audit lacks %s: %s", want, buf)
				}
			}
			if strings.Contains(buf.String(), "alice@example.com") {
				t.Fatal("the email reached the audit record")
			}
		})
	}
}
