package adminui

import (
	"os/exec"
	"testing"
)

// Tests the shipped scripts and their rendered DOM, not source text.
func TestAnalyticsEvidenceBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("analytics evidence behavior requires Node (standard library only)")
	}
	out, err := exec.Command(node, "testdata/analytics_evidence.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("analytics evidence regression: %v\n%s", err, out)
	}
}
