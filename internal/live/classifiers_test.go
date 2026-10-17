package live

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/config"
)

type liveTestClassifier struct{ cfg classifier.Config }

func (liveTestClassifier) Windows(s []string) []string { return s }
func (liveTestClassifier) Score(context.Context, []string) (classifier.Scores, error) {
	return classifier.Scores{}, nil
}

func init() {
	classifier.Register("live-test", func(c classifier.Config) (classifier.Classifier, error) {
		if c.Options["fail"] != nil {
			return nil, errors.New("live-test: bad option")
		}
		return liveTestClassifier{cfg: c}, nil
	})
}

func withClassifiers(entries ...config.ClassifierConfig) *config.Config {
	cfg := sampleConfig()
	cfg.PIIClassification = &config.PIIClassificationConfig{Classifiers: entries}
	return cfg
}

func liveEntry(name, mode string, teams ...string) config.ClassifierConfig {
	return config.ClassifierConfig{Name: name, Type: "live-test", Mode: mode, Teams: teams, Endpoint: "http://127.0.0.1:8095", DataBoundary: "internal"}
}

func TestBuildStateWithoutClassifiersHasNilSet(t *testing.T) {
	st, _, err := BuildState(sampleConfig())
	if err != nil || st.Classifiers() != nil {
		t.Fatalf("%v %v", st.Classifiers(), err)
	}
	var none *State
	if none.Classifiers() != nil {
		t.Fatal("nil State returned a Set")
	}
}

func TestBuildStateBuildsTheClassifierSet(t *testing.T) {
	t.Setenv("LIVE_TEST_CLASSIFIER_TOKEN", "token-value-123")
	scoped := liveEntry("scoped", "Enforce", "kr")
	scoped.APIKeyRef = &config.SecretRef{Env: "LIVE_TEST_CLASSIFIER_TOKEN"}
	st, _, err := BuildState(withClassifiers(liveEntry("global", "Shadow"), scoped))
	if err != nil {
		t.Fatal(err)
	}
	set := st.Classifiers()
	kr, other := set.For("kr"), set.For("us")
	if len(kr) != 2 || len(other) != 1 || other[0].Name != "global" || kr[1].Mode != classifier.Enforce {
		t.Fatalf("scoping kr=%+v us=%+v", kr, other)
	}
	built := kr[1].Classifier.(liveTestClassifier).cfg
	if kr[0].Settings == ([32]byte{}) || kr[1].Settings != classifier.SettingsDigest(built) {
		t.Fatal("entries do not carry the digest of their built config")
	}
	if built.BearerToken != "token-value-123" || built.Threshold != 0.5 || set.Timeout.Milliseconds() != 500 || set.MaxInputBytes != 2<<20 {
		t.Fatalf("built config %+v, set %+v", built, set)
	}
}

func TestBuildStateRejectsBadClassifiers(t *testing.T) {
	unknown := liveEntry("unknown", "Shadow")
	unknown.Type = "nope"
	badOption := liveEntry("bad", "Shadow")
	badOption.Options = map[string]json.RawMessage{"fail": json.RawMessage("true")}
	unset := liveEntry("unset", "Shadow")
	unset.APIKeyRef = &config.SecretRef{Env: "LIVE_TEST_CLASSIFIER_UNSET"}
	// A token file that holds only whitespace resolves to an empty token; like
	// an empty env token, it must fail the build instead of running unauthenticated.
	blankFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(blankFile, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blank := liveEntry("blank", "Shadow")
	blank.APIKeyRef = &config.SecretRef{File: blankFile}
	for name, tc := range map[string]struct {
		entry config.ClassifierConfig
		want  string
	}{
		"unknown type": {unknown, "unknown type"},
		"bad option":   {badOption, "bad option"},
		"unset token":  {unset, "LIVE_TEST_CLASSIFIER_UNSET"},
		"empty token":  {blank, "resolved to an empty token"},
	} {
		st, _, err := BuildState(withClassifiers(tc.entry))
		if err == nil || !strings.Contains(err.Error(), tc.want) || st != nil {
			t.Errorf("%s: state %v, err = %v", name, st != nil, err)
		}
	}
}

func TestEveryBuildGetsAFreshSet(t *testing.T) {
	cfg := withClassifiers(liveEntry("global", "Shadow"))
	a, _, errA := BuildState(cfg)
	b, _, errB := BuildState(cfg)
	if errA != nil || errB != nil || a.Classifiers() == b.Classifiers() {
		t.Fatal("generations must not share a Set or its cache")
	}
}
