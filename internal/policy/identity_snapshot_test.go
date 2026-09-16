package policy

import (
	"encoding/json"
	"errors"
	"testing"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/identity"
)

func TestRequiredIdentityKeepsBudgetIfTopologyChangesDuringValidation(t *testing.T) {
	id, _ := identity.NewHuman("acme", "https://idp.example", "person")
	s := NewEmptyStore()
	if err := s.SetIdentityConfig(&identity.Config{Organization: "acme", Required: true}); err != nil {
		t.Fatal(err)
	}
	docs := identityPolicy(t, id.CanonicalRef())
	if rejected := s.ApplyWire(docs); len(rejected) != 0 {
		t.Fatal(rejected)
	}
	before := s.snap.Load()
	var privacy v1alpha1.Rule
	if err := json.Unmarshal([]byte(`{"name":"privacy","failurePolicy":"FailClosed","sensitiveData":{"onDetected":"InternalOnly","onUninspectable":"Block","internalModels":["private"]}}`), &privacy); err != nil {
		t.Fatal(err)
	}
	docs[0].Spec.Rules = append(docs[0].Spec.Rules, privacy)
	calls := 0
	s.SetRoutedAndPriced(func(string) error {
		calls++
		if calls > 1 {
			return errors.New("topology changed")
		}
		return nil
	})
	if rejected := s.ApplyWire(docs); len(rejected) == 0 || calls < 2 {
		t.Fatal("test did not exercise the second-pass topology rejection")
	}
	if s.snap.Load() != before {
		t.Fatal("second-pass rejection discarded a valid budget snapshot")
	}
}
