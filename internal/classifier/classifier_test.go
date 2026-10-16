package classifier

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type stubClassifier struct{}

func (stubClassifier) Units(s []string) []string                       { return s }
func (stubClassifier) Classify(context.Context, Input) (Output, error) { return Output{}, nil }

func stubFactory(Config) (Classifier, error) { return stubClassifier{}, nil }

// Registered once per process so the tests survive -count=N.
func init() {
	Register("classifier-test-dup", stubFactory)
	Register("classifier-test-known", stubFactory)
}

func TestRegisterRejectsDuplicateEmptyAndNil(t *testing.T) {
	for name, call := range map[string]func(){
		"duplicate":   func() { Register("classifier-test-dup", stubFactory) },
		"empty type":  func() { Register("", stubFactory) },
		"nil factory": func() { Register("classifier-test-nil", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			call()
		})
	}
}

func TestNewUnknownTypeNamesRegisteredTypes(t *testing.T) {
	_, err := New(Config{Name: "x", Type: "nope"})
	if err == nil || !strings.Contains(err.Error(), `unknown type "nope"`) || !strings.Contains(err.Error(), "classifier-test-known") {
		t.Fatalf("err = %v", err)
	}
}

func TestSettingsDigestCoversAnswerSettings(t *testing.T) {
	base := Config{Type: "systemone", Endpoint: "http://127.0.0.1:8095", Revision: "r1", Threshold: 0.5,
		Options: map[string]json.RawMessage{"model": json.RawMessage(`"multilingual"`), "max_windows": json.RawMessage(`4`)}}
	want := SettingsDigest(base)
	same := base
	same.Options = map[string]json.RawMessage{"max_windows": json.RawMessage(` 4 `), "model": json.RawMessage(`"multilingual"`)}
	same.BearerToken = "rotated-bearer"
	if SettingsDigest(same) != want {
		t.Fatal("whitespace, option order or a rotated token changed the digest")
	}
	for name, edit := range map[string]func(*Config){
		"type":      func(c *Config) { c.Type = "other" },
		"endpoint":  func(c *Config) { c.Endpoint = "http://127.0.0.1:8096" },
		"revision":  func(c *Config) { c.Revision = "r2" },
		"threshold": func(c *Config) { c.Threshold = 0.6 },
		"option": func(c *Config) {
			c.Options = map[string]json.RawMessage{"model": json.RawMessage(`"english"`), "max_windows": json.RawMessage(`4`)}
		},
	} {
		c := base
		edit(&c)
		if SettingsDigest(c) == want {
			t.Errorf("a %s change kept the digest", name)
		}
	}
}

func TestForScopesEntries(t *testing.T) {
	var none *Set
	if none.For("kr") != nil {
		t.Fatal("a nil Set returned entries")
	}
	s, err := NewSet([]Entry{{Name: "global", Global: true}, {Name: "scoped", Teams: map[string]bool{"kr": true}}}, time.Second, 1<<20, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for team, want := range map[string]string{"kr": "global,scoped", "other": "global", "": "global"} {
		if got := entryNames(s.For(team)); got != want {
			t.Errorf("For(%q) = %s, want %s", team, got, want)
		}
	}
}

func entryNames(entries []Entry) string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return strings.Join(out, ",")
}
