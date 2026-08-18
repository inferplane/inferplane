package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	sigyaml "sigs.k8s.io/yaml"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
)

// docSep splits a multi-document YAML stream on the standard `---` separator
// line, so one file can carry several GovernancePolicy documents kubectl-style.
var docSep = regexp.MustCompile(`(?m)^---\s*$`)

// parseWireDocs parses one YAML (or JSON — YAML is a superset) stream into
// wire documents, schema-validating each through FromV1Alpha1. Unmarshalling
// is STRICT: an unknown field is a version-skew symptom and is rejected,
// never silently dropped — a document written for a newer schema must fail
// loudly here, not lose its newest field.
func parseWireDocs(data []byte) ([]v1alpha1.GovernancePolicy, error) {
	var out []v1alpha1.GovernancePolicy
	for i, doc := range docSep.Split(string(data), -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var wire v1alpha1.GovernancePolicy
		if err := sigyaml.UnmarshalStrict([]byte(doc), &wire); err != nil {
			return nil, fmt.Errorf("document %d: %w", i+1, err)
		}
		if _, err := FromV1Alpha1(&wire); err != nil {
			return nil, fmt.Errorf("document %d: %w", i+1, err)
		}
		out = append(out, wire)
	}
	return out, nil
}

// ParseWireDocs parses a YAML/JSON stream into wire documents (see
// parseWireDocs for the strictness stance) without converting to the
// internal form. Exported for a write path that needs the wire shape itself
// (to persist verbatim and read metadata.name) but must still share this
// package's single validation path — never a hand-rolled parse-and-check.
func ParseWireDocs(data []byte) ([]v1alpha1.GovernancePolicy, error) {
	return parseWireDocs(data)
}

// ParseDocs parses a YAML/JSON stream of GovernancePolicy documents into the
// internal form (see parseWireDocs for the strictness stance).
func ParseDocs(data []byte) ([]*Policy, error) {
	wires, err := parseWireDocs(data)
	if err != nil {
		return nil, err
	}
	out := make([]*Policy, 0, len(wires))
	for i := range wires {
		p, err := FromV1Alpha1(&wires[i])
		if err != nil {
			return nil, err // unreachable: parseWireDocs already validated
		}
		out = append(out, p)
	}
	return out, nil
}

// readPolicyFile reads one policy file with path context on error.
func readPolicyFile(f string) ([]byte, error) {
	data, err := os.ReadFile(f)
	if err != nil {
		return nil, fmt.Errorf("policy file %s: %w", f, err)
	}
	return data, nil
}

// enumerate lists the policy files under the given paths. A path may be a
// file or a directory; directories are read one level deep, taking
// *.yaml / *.yml / *.json in sorted order.
func enumerate(paths ...string) ([]string, error) {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("policy path %s: %w", p, err)
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("policy dir %s: %w", p, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			switch filepath.Ext(e.Name()) {
			case ".yaml", ".yml", ".json":
				files = append(files, filepath.Join(p, e.Name()))
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// Enumerate is the exported enumeration used by external watchers
// (internal/controlplane) to poll the same file set LoadPaths would read.
func Enumerate(paths ...string) ([]string, error) { return enumerate(paths...) }

// LoadPaths reads GovernancePolicy documents from the given paths (see
// enumerate for path semantics). Policy names must be unique across
// everything loaded. The returned file list is what a watcher should poll.
func LoadPaths(paths ...string) ([]*Policy, []string, error) {
	files, err := enumerate(paths...)
	if err != nil {
		return nil, nil, err
	}

	var out []*Policy
	seen := make(map[string]string) // policy name → file, for duplicate detection
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, fmt.Errorf("policy file %s: %w", f, err)
		}
		docs, err := ParseDocs(data)
		if err != nil {
			return nil, nil, fmt.Errorf("policy file %s: %w", f, err)
		}
		for _, p := range docs {
			if p.Name == "" {
				return nil, nil, fmt.Errorf("policy file %s: metadata.name is required", f)
			}
			if prev, dup := seen[p.Name]; dup {
				return nil, nil, fmt.Errorf("policy file %s: duplicate policy name %q (already defined in %s)", f, p.Name, prev)
			}
			seen[p.Name] = f
			out = append(out, p)
		}
	}
	return out, files, nil
}
