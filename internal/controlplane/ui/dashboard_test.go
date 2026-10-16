package ui

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/policy"
)

// No network, credentials, browser, or npm packages: execute the embedded app's
// real handlers at their DOM and fetch boundaries. Layout/CSP QA is separate.
func TestDashboardBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("dashboard behavior checks need Node (stdlib only)")
	}
	out, err := exec.Command(node, "testdata/dashboard.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("dashboard regression: %v\n%s", err, out)
	}
}

func TestSSOSessionLifecycle(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("SSO session checks need Node (stdlib only)")
	}
	out, err := exec.Command(node, "testdata/sso_session.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("SSO session regression: %v\n%s", err, out)
	}
}

func TestCostFirstLayoutBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("cost-first layout checks need Node (stdlib only)")
	}
	out, err := exec.Command(node, "testdata/charts.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("cost-first layout regression: %v\n%s", err, out)
	}
}

func TestBudgetWorkflow(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("budget workflow checks need Node (stdlib only)")
	}
	out, err := exec.Command(node, "testdata/budgets.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("budget workflow regression: %v\n%s", err, out)
	}
}

func TestWorkspaceWorkflow(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("workspace workflow checks need Node (stdlib only)")
	}
	out, err := exec.Command(node, "testdata/workspace.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("workspace workflow regression: %v\n%s", err, out)
	}
}

// The same shipped handlers exercised above must also produce documents the
// actual server's strict decoder and policy validator accept.
func TestGuidedRulesUsePolicyContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("guided-rule contract checks need Node (stdlib only)")
	}
	out, err := exec.Command(node, "testdata/workspace.cjs", "--payloads").CombinedOutput()
	if err != nil {
		t.Fatalf("guided-rule payloads: %v\n%s", err, out)
	}
	var documents []json.RawMessage
	if err := json.Unmarshal(out, &documents); err != nil {
		t.Fatalf("decode generated documents: %v", err)
	}
	if len(documents) < 5 {
		t.Fatalf("missing generated rule cases: got %d documents", len(documents))
	}
	for i, raw := range documents {
		docs, err := policy.ParseWireDocs(raw)
		if err != nil || len(docs) != 1 {
			t.Fatalf("document %d: strict decode %v (count %d)", i, err, len(docs))
		}
		if _, err := policy.FromV1Alpha1(&docs[0]); err != nil {
			t.Fatalf("document %d: rule semantics: %v", i, err)
		}
	}
}

func TestUserCostAndLimitWorkflow(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("user cost and limit checks need Node (stdlib only)")
	}
	out, err := exec.Command(node, "testdata/user_limits.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("user cost and limit regression: %v\n%s", err, out)
	}
}

func TestUserGuidesRespectDataPlaneProfiles(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("user-guide contract checks need Node (stdlib only)")
	}
	out, err := exec.Command(node, "testdata/user_limits.cjs", "--payloads").CombinedOutput()
	if err != nil {
		t.Fatalf("user-guide payloads: %v\n%s", err, out)
	}
	var documents []json.RawMessage
	if err := json.Unmarshal(out, &documents); err != nil {
		t.Fatal(err)
	}
	quotas := 0
	for i, raw := range documents {
		docs, err := policy.ParseWireDocs(raw)
		if err != nil || len(docs) != 1 {
			t.Fatalf("document %d: strict decode %v", i, err)
		}
		if _, err := policy.FromV1Alpha1(&docs[0]); err != nil {
			t.Fatalf("document %d: semantics %v", i, err)
		}
		shared := policy.NewEmptyStore()
		shared.SetSharedEnforcement(true) // synthetic admission profile, no DB/network
		if rejected := shared.ApplyWire(docs); len(rejected) != 0 {
			t.Fatalf("document %d rejected by shared profile: %+v", i, rejected)
		}
		requiresShared := false
		for _, rule := range docs[0].Spec.Rules {
			if rule.Rate != nil && (docs[0].Spec.Subject.User != "" || docs[0].Spec.Subject.Team == "") {
				requiresShared = true
			}
			if rule.TokenQuota != nil {
				quotas++
				requiresShared = true
				if rule.FailurePolicy != v1alpha1.FailClosed {
					t.Fatal("quota guide weakened fail-closed enforcement")
				}
			}
		}
		if requiresShared {
			local := policy.NewEmptyStore()
			if rejected := local.ApplyWire(docs); len(rejected) != 1 {
				t.Fatalf("document %d unexpectedly accepted in local profile", i)
			}
		}
	}
	if quotas < 3 {
		t.Fatal("missing create/edit/int64 token-quota cases")
	}
}
