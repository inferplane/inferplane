package adminui

import (
	"os/exec"
	"testing"
)

// Executes shipped form handlers and observes their fetch payloads; it does not
// assert on source text. Node's standard library is the only test prerequisite.
func TestRoutingFormBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("form behavior test needs Node (no npm packages); browser QA also covers this flow")
	}
	out, err := exec.Command(node, "testdata/routing_forms.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("console form regression: %v\n%s", err, out)
	}
}

func TestMoneyFormBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("money form behavior test needs Node (no npm packages)")
	}
	out, err := exec.Command(node, "testdata/routing_forms.cjs", "--money").CombinedOutput()
	if err != nil {
		t.Fatalf("console money regression: %v\n%s", err, out)
	}
}

func TestViewReadBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("view navigation behavior test needs Node (no npm packages)")
	}
	out, err := exec.Command(node, "testdata/routing_forms.cjs", "--views").CombinedOutput()
	if err != nil {
		t.Fatalf("console navigation regression: %v\n%s", err, out)
	}
}
