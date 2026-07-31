package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoDocYAML = `apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: platform-eng
spec:
  subject: { team: platform-eng }
  rules:
  - name: monthly-hard-cap
    failurePolicy: FailClosed
    budget: { limitMilliUSD: 5000000, hardCap: true }
---
apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata:
  name: junseok-models
spec:
  subject: { user: junseok }
  rules:
  - name: models
    failurePolicy: FailOpen
    modelAccess: { allow: ["claude-haiku-4-5"] }
`

func TestParseDocsMultiDocYAML(t *testing.T) {
	ps, err := ParseDocs([]byte(twoDocYAML))
	if err != nil {
		t.Fatalf("ParseDocs: %v", err)
	}
	if len(ps) != 2 || ps[0].Name != "platform-eng" || ps[1].Name != "junseok-models" {
		t.Fatalf("got %+v", ps)
	}
	if ps[0].Rules[0].Budget.LimitMicroUSD != 5_000_000_000 {
		t.Fatalf("milliUSD→µUSD conversion wrong: %d", ps[0].Rules[0].Budget.LimitMicroUSD)
	}
}

// An unknown field is a version-skew symptom (a doc written for a newer
// schema) and MUST fail loudly, never lose the field silently.
func TestParseDocsRejectsUnknownField(t *testing.T) {
	doc := strings.Replace(twoDocYAML, "hardCap: true", "hardCap: true, futureKnob: 7", 1)
	if _, err := ParseDocs([]byte(doc)); err == nil {
		t.Fatal("unknown field accepted — silent version skew")
	}
}

func TestParseDocsJSON(t *testing.T) {
	j := `{"apiVersion":"inferplane.dev/v1alpha1","kind":"GovernancePolicy",
	 "metadata":{"name":"j"},
	 "spec":{"subject":{"team":"t"},"rules":[
	   {"name":"r","failurePolicy":"FailOpen","rate":{"rpm":10}}]}}`
	ps, err := ParseDocs([]byte(j))
	if err != nil {
		t.Fatalf("ParseDocs(json): %v", err)
	}
	if len(ps) != 1 || ps[0].Rules[0].Rate.RPM != 10 {
		t.Fatalf("got %+v", ps)
	}
}

func TestLoadPathsDirAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(twoDocYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("not a policy"), 0o600); err != nil {
		t.Fatal(err)
	}
	ps, files, err := LoadPaths(dir)
	if err != nil {
		t.Fatalf("LoadPaths: %v", err)
	}
	if len(ps) != 2 || len(files) != 1 {
		t.Fatalf("got %d policies from %d files", len(ps), len(files))
	}

	// The same policy name in a second file must be rejected.
	if err := os.WriteFile(filepath.Join(dir, "b.yml"), []byte(twoDocYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPaths(dir); err == nil || !strings.Contains(err.Error(), "duplicate policy name") {
		t.Fatalf("duplicate names accepted: %v", err)
	}
}

// The shipped example must always parse and pass the enforceability gate —
// documentation that rots fails here, not on a user's machine.
func TestExamplePoliciesLoad(t *testing.T) {
	if _, err := NewStore("../../examples/policies"); err != nil {
		t.Fatalf("examples/policies: %v", err)
	}
}
