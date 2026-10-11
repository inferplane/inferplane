package ui

import (
	"os/exec"
	"testing"
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
