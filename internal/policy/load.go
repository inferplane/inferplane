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

// ParseDocs parses one YAML (or JSON — YAML is a superset) stream of
// GovernancePolicy documents. Unmarshalling is STRICT: an unknown field is a
// version-skew symptom and is rejected, never silently dropped — a document
// written for a newer schema must fail loudly here, not lose its newest field.
func ParseDocs(data []byte) ([]*Policy, error) {
	var out []*Policy
	for i, doc := range docSep.Split(string(data), -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var wire v1alpha1.GovernancePolicy
		if err := sigyaml.UnmarshalStrict([]byte(doc), &wire); err != nil {
			return nil, fmt.Errorf("document %d: %w", i+1, err)
		}
		p, err := FromV1Alpha1(&wire)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i+1, err)
		}
		out = append(out, p)
	}
	return out, nil
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
