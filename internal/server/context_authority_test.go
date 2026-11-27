package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/internal/tokenest"
	"github.com/inferplane/inferplane/pkg/schema"
)

// The gateway's token count is an estimate; an upstream that enforces the
// window counts exactly. When the target's upstream is the context authority,
// a request the estimate puts over the declared window must still reach it,
// on every ingress. Without the capability the gateway keeps its own check.
func TestUpstreamContextAuthorityDecidesOverflow(t *testing.T) {
	text := strings.Repeat("word ", 3000)
	ingresses := append(append([]policyIngress(nil), policyIngresses...),
		policyIngress{"responses", "/v1/responses", false}, policyIngress{"responses", "/v1/responses", true})
	for _, ing := range ingresses {
		for _, authority := range []bool{true, false} {
			t.Run(ing.name()+"/authority="+strconv.FormatBool(authority), func(t *testing.T) {
				f := routingtest.New(t, func(c *config.Config) {
					m := c.Models["premium"]
					m.ContextWindow = 2000
					c.Models["premium"] = m
				})
				in, out := int64(5), int64(1)
				f.Public.Usage = &schema.Usage{InputTokens: &in, OutputTokens: &out}
				f.Public.ContextAuthority = authority
				stream := strconv.FormatBool(ing.stream)
				body := `{"model":"premium","max_tokens":16,"stream":` + stream + `,"messages":[{"role":"user","content":"` + text + `"}]}`
				if ing.protocol == "responses" {
					body = `{"model":"premium","stream":` + stream + `,"store":false,"max_output_tokens":16,"input":"` + text + `"}`
				}
				protocol := ing.protocol
				if protocol == "openai" {
					protocol = "chat"
				}
				if est := tokenest.Estimate(protocol, []byte(body)); est <= 2000 {
					t.Fatalf("fixture estimate %d must exceed the 2000-token window", est)
				}
				rec := policyDo(policyMux(f, nil, nil, nil, nil), ing.path, body, true)
				wantStatus, wantCalls := http.StatusOK, 1
				if !authority {
					wantStatus, wantCalls = http.StatusBadRequest, 0
				}
				if rec.Code != wantStatus || len(f.Public.Calls) != wantCalls {
					t.Fatalf("status=%d calls=%d, want %d/%d: %s", rec.Code, len(f.Public.Calls), wantStatus, wantCalls, rec.Body.String())
				}
			})
		}
	}
}
