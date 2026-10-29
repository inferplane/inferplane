package live

import "testing"

func TestFallbackSourceNamesWhereTheSubstitutionComesFrom(t *testing.T) {
	cfg := familyConfig()
	cfg.ModelFallbacks = map[string]string{"claude-haiku-9": "claude-sonnet-4-6"}
	st, _, err := BuildState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ requested, served, source string }{
		{"claude-haiku-9", "claude-sonnet-4-6", "model_fallbacks"},
		{"claude-opus-9", "claude-opus-4-8", "family"},
		{"claude-opus-4-2", "", ""},
		{"unrelated", "", ""},
	} {
		served, source := st.FallbackSource(tc.requested)
		if served != tc.served || source != tc.source || st.FallbackFor(tc.requested) != tc.served {
			t.Fatalf("FallbackSource(%q) = (%q, %q), want (%q, %q)", tc.requested, served, source, tc.served, tc.source)
		}
	}
}
