package requestpolicy

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/router"
)

func TestObserveProjectsClassifierRefs(t *testing.T) {
	result := router.RequestRoutingResult{Decision: router.RoutingDecision{
		RequestedModel: "m", Reason: "unchanged", Inspection: "complete",
		Policies:    []router.RoutingPolicyRef{{Name: "privacy", Generation: 7, Rule: "protect"}},
		Classifiers: []router.ClassifierRef{{Name: "laya-ko", Revision: "r1", Boundary: "internal", Mode: "Shadow", Result: "abstain", WouldRestrict: true}},
	}}
	req := Observe(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil), result, nil)
	got := audit.RoutingFrom(req.Context())
	want := []audit.ClassifierRef{{Name: "laya-ko", Revision: "r1", Boundary: "internal", Mode: "Shadow", Result: "abstain", WouldRestrict: true}}
	if got == nil || !reflect.DeepEqual(got.Classifiers, want) {
		t.Fatalf("%+v", got)
	}
}
