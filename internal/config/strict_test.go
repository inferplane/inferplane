package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadReviewConfig(t *testing.T, raw string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return LoadRaw(path)
}

func TestLoadRawRejectsUnknownTypedFields(t *testing.T) {
	for _, tc := range []struct {
		raw, field string
	}{
		{`{"governance_stroe":{}}`, "governance_stroe"},
		{`{"server":{"admin_auth":{"token_ref":[]}}}`, "token_ref"},
		{`{"teams":{"demo":{"budget":{"usd_per_mont":5}}}}`, "usd_per_mont"},
		{`{"teams":{"demo":{"rate_limit":{"request_per_minute":1}}}}`, "request_per_minute"},
		{`{"providers":{"demo":{"auth":{"mdoe":"broker"}}}}`, "mdoe"},
		{`{"models":{"demo":{"targets":[{"providre":"demo"}]}}}`, "providre"},
		{`{"key_store":{"identity":{"organization":"demo","requird":true}}}`, "requird"},
		{`{"audit":{"sinks":[{"type":"stdout","requird":true}]}}`, "requird"},
		{`{"Server":{"listen":":8080"}}`, "Server"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			_, err := loadReviewConfig(t, tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("LoadRaw accepted an unknown enforcement field or hid its path: %v", err)
			}
		})
	}
}

func TestLoadRawPreservesAnnotationsAndFreeFormNames(t *testing.T) {
	cfg, err := loadReviewConfig(t, `{
		"_comment": "operator note",
		"_otel_example": {"not_an_active_setting": true},
		"server": {"_comment_auth": "intentional annotation"},
		"teams": {"_comment": {"allowed_models": ["demo"]}},
		"models": {
			"custom-name": {"targets": [{"provider": "custom-name", "model": "demo"}]},
			"another-custom-name": {"targets": [{"provider": "custom-name", "model": "demo-small"}]}
		},
		"providers": {"custom-name": {"type": "openai_compatible"}},
		"model_fallbacks": {"custom-name": "another-custom-name"}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Teams["_comment"]; !ok {
		t.Fatal("annotation handling consumed a legitimate free-form map key")
	}
}

func TestLoadRawPreservesClassifierAnnotationsAndPluginOptions(t *testing.T) {
	cfg, err := loadReviewConfig(t, `{"pii_classification":{
		"_comment": "keep the classifier in Shadow",
		"classifiers":[{
			"name":"local", "type":"systemone", "mode":"Shadow",
			"endpoint":"http://127.0.0.1:8095", "data_boundary":"internal",
			"options":{"custom_option":{"opaque_plugin_field":true}}
		}]
	}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.PIIClassification.Classifiers[0].Options["custom_option"]; !ok {
		t.Fatal("typed config validation discarded plugin-owned options")
	}
}

func TestLoadRawRejectsInvalidTeamLimits(t *testing.T) {
	for _, raw := range []string{
		`{"rate_limit":{"requests_per_minute":-1}}`,
		`{"rate_limit":{"tokens_per_minute":-1}}`,
		`{"quota":{"tokens_per_day":-1}}`,
		`{"quota":{"tokens_per_month":-1}}`,
		`{"quota":{"tokens_per_month":1000}}`,
		`{"quota":{"on_exceeded":"blok"}}`,
		`{"budget":{"usd_per_day":-1}}`,
		`{"budget":{"usd_per_month":-1}}`,
		`{"budget":{"usd_per_month":1e20}}`,
		`{"budget":{"on_exceeded":"allow"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := loadReviewConfig(t, `{"teams":{"demo":`+raw+`}}`)
			if err == nil || !strings.Contains(err.Error(), "teams") {
				t.Fatalf("invalid team limit silently disables or changes enforcement: %v", err)
			}
		})
	}
}

func TestLoadRawAcceptsTeamLimitDefaultsAndActions(t *testing.T) {
	for _, action := range []string{"", "block", "warn"} {
		if _, err := loadReviewConfig(t, `{"teams":{"demo":{"budget":{"usd_per_day":1,"on_exceeded":"`+action+`"},"quota":{"tokens_per_day":100,"on_exceeded":"`+action+`"}}}}`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadKeyStoreRejectsUnknownBackendSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"key_store":{"type":"sqlite","paht":"/var/lib/keys.db"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyStore(path); err == nil || !strings.Contains(err.Error(), "paht") {
		t.Fatalf("offline key CLI silently selects a different store: %v", err)
	}
}

func TestLoadRawRejectsUnsupportedAuditFailureMode(t *testing.T) {
	for _, mode := range []string{"allow", "buffer_then_blok", "block"} {
		if _, err := loadReviewConfig(t, `{"audit":{"failure_mode":"`+mode+`"}}`); err == nil || !strings.Contains(err.Error(), "audit.failure_mode") {
			t.Fatalf("unsupported audit mode %q silently accepted: %v", mode, err)
		}
	}
	for _, mode := range []string{"", "buffer_then_block"} {
		if _, err := loadReviewConfig(t, `{"audit":{"failure_mode":"`+mode+`"}}`); err != nil {
			t.Fatal(err)
		}
	}
}
