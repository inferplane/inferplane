package anthropicapi

import (
	"encoding/json"
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

// Ruling 13 at the handler: for a team with an Enforce classifier and an
// applicable sensitiveData rule, a count makes no classifier call, treats the
// body as uninspectable and still answers HTTP 200. Under Block it is the
// local estimate; under InternalOnly only the internal counter sees the body.
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
			raw := routingtest.Body("anthropic", "plain", false) // no regex detection
			rec := httptest.NewRecorder()
			NewCountTokensHandler(f.Router).ServeHTTP(rec, routingtest.Request("/v1/messages/count_tokens", raw))
			var out struct {
				InputTokens int64 `json:"input_tokens"`
			}
			if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
				t.Fatalf("count_tokens must stay 200: %d %s", rec.Code, rec.Body)
			}
			if calls.Load() != 0 {
				t.Fatalf("a count called the classifier %d times", calls.Load())
			}
			if len(f.Public.Calls) != 0 {
				t.Fatal("an unread count body reached the external provider")
			}
			switch action {
			case v1alpha1.Block:
				if out.InputTokens != estimateTokens([]byte(raw)) || len(f.Private.Calls) != 0 {
					t.Fatalf("blocked count: %d tokens, %d private calls; want the local estimate %d", out.InputTokens, len(f.Private.Calls), estimateTokens([]byte(raw)))
				}
			case v1alpha1.InternalOnly:
				if out.InputTokens != 777 || len(f.Private.Calls) != 1 {
					t.Fatalf("internal count: %d tokens, %d private calls", out.InputTokens, len(f.Private.Calls))
				}
			}
		})
	}
}
