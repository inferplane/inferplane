package bedrockapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/policy"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	_ "github.com/inferplane/inferplane/plugins/systemone"
)

// Ruling 13 on the Bedrock ingress: see the anthropicapi test of the same name.
func TestCountTokensWithAnEnforceClassifierMakesNoCall(t *testing.T) {
	var calls atomic.Int64
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(sidecar.Close)
	for _, action := range []v1alpha1.SensitiveDataAction{v1alpha1.Block, v1alpha1.InternalOnly} {
		t.Run(string(action), func(t *testing.T) {
			f := routingtest.New(t, func(cfg *config.Config) {
				cfg.PIIClassification = &config.PIIClassificationConfig{Classifiers: []config.ClassifierConfig{{
					Name: "laya-ko", Type: "systemone", Mode: "Enforce", Teams: []string{"team"}, Endpoint: sidecar.URL, DataBoundary: "internal",
				}}}
			})
			f.Policies = []*policy.Policy{routingtest.Privacy(action)}
			inner := routingtest.Body("bedrock", "plain", false) // no regex detection
			rec := httptest.NewRecorder()
			NewCountTokensHandler(f.Router, f.Holder).ServeHTTP(rec, routingtest.Request("/model/premium/count-tokens", countTokensBody(t, inner)))
			got := assert200InputTokens(t, rec)
			if calls.Load() != 0 {
				t.Fatalf("a count called the classifier %d times", calls.Load())
			}
			if len(f.Public.Calls) != 0 {
				t.Fatal("an unread count body reached the external provider")
			}
			switch action {
			case v1alpha1.Block:
				if got != estimateTokens([]byte(inner)) || len(f.Private.Calls) != 0 {
					t.Fatalf("blocked count: %d tokens, %d private calls; want the local estimate %d", got, len(f.Private.Calls), estimateTokens([]byte(inner)))
				}
			case v1alpha1.InternalOnly:
				if got != 777 || len(f.Private.Calls) != 1 || f.Private.Calls[0].Body != inner {
					t.Fatalf("internal count: %d tokens, %d private calls", got, len(f.Private.Calls))
				}
			}
		})
	}
}
