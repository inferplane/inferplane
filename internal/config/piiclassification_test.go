package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadPII(t *testing.T, block string) (*Config, error) {
	t.Helper()
	body := `{"providers":{"p":{"type":"anthropic"}}`
	if block != "" {
		body += `,"pii_classification":` + block
	}
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, []byte(body+"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadRaw(path)
}

// entry returns one classifier object; a nil value removes a base field.
func entry(over map[string]any) string {
	e := map[string]any{"name": "laya-ko", "type": "systemone", "mode": "Shadow", "endpoint": "http://127.0.0.1:8095", "data_boundary": "internal"}
	for k, v := range over {
		if v == nil {
			delete(e, k)
		} else {
			e[k] = v
		}
	}
	b, _ := json.Marshal(e)
	return string(b)
}

func classifiers(entries ...string) string {
	return `{"classifiers":[` + strings.Join(entries, ",") + `]}`
}

func TestPIIClassificationAbsentIsNil(t *testing.T) {
	cfg, err := loadPII(t, "")
	if err != nil || cfg.PIIClassification != nil {
		t.Fatalf("%+v %v", cfg.PIIClassification, err)
	}
}

func TestPIIClassificationDefaults(t *testing.T) {
	cfg, err := loadPII(t, classifiers(entry(nil)))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.PIIClassification
	if p.Timeout() != 500*time.Millisecond || p.InputCap() != 2<<20 || p.CacheEntries() != 100_000 || p.CacheTTL() != time.Hour || p.Classifiers[0].EffectiveThreshold() != 0.5 {
		t.Fatalf("defaults: %v %d %d %v %v", p.Timeout(), p.InputCap(), p.CacheEntries(), p.CacheTTL(), p.Classifiers[0].EffectiveThreshold())
	}
	off, err := loadPII(t, `{"cache":{"max_entries":0},"classifiers":[`+entry(nil)+`]}`)
	if err != nil || off.PIIClassification.CacheEntries() != 0 {
		t.Fatalf("explicit max_entries 0 must disable the cache: %v", err)
	}
}

func TestPIIClassificationRejects(t *testing.T) {
	five := classifiers(entry(map[string]any{"name": "a"}), entry(map[string]any{"name": "b"}), entry(map[string]any{"name": "c"}), entry(map[string]any{"name": "d"}), entry(map[string]any{"name": "e"}))
	for block, want := range map[string]string{
		`{"classifiers":[]}`: "at least one",
		five:                 "at most 4",
		classifiers(entry(map[string]any{"name": "Laya"})):                                        "name",
		classifiers(entry(nil), entry(nil)):                                                       "duplicate",
		classifiers(entry(map[string]any{"type": nil})):                                           "type",
		classifiers(entry(map[string]any{"revision": "R1!"})):                                     "revision",
		classifiers(entry(map[string]any{"mode": "enforce"})):                                     "mode",
		classifiers(entry(map[string]any{"data_boundary": "external"})):                           "not supported",
		classifiers(entry(map[string]any{"data_boundary": nil})):                                  "data_boundary",
		classifiers(entry(map[string]any{"endpoint": "ftp://127.0.0.1"})):                         "endpoint",
		classifiers(entry(map[string]any{"endpoint": "http://127.0.0.1:1/?k=v"})):                 "query",
		classifiers(entry(map[string]any{"threshold": 1})):                                        "threshold",
		classifiers(entry(map[string]any{"teams": []string{""}})):                                 "teams",
		classifiers(entry(map[string]any{"api_key": "inline-value-123"})):                         "inline api_key",
		classifiers(entry(map[string]any{"api_key_ref": map[string]string{"env": "not a name"}})): "api_key_ref",
		classifiers(entry(map[string]any{"endpont": "http://127.0.0.1:1"})):                       "unknown field",
		`{"timeout_ms":30001,"classifiers":[` + entry(nil) + `]}`:                                 "timeout_ms",
		`{"max_input_bytes":100,"classifiers":[` + entry(nil) + `]}`:                              "max_input_bytes",
		`{"cache":{"max_entries":-1},"classifiers":[` + entry(nil) + `]}`:                         "max_entries",
		`{"cache":{"ttl_seconds":10},"classifiers":[` + entry(nil) + `]}`:                         "ttl_seconds",
		`{"cache":{"max_entries":10,"ttl":60},"classifiers":[` + entry(nil) + `]}`:                "unknown field",
	} {
		if _, err := loadPII(t, block); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("block %s: err = %v, want %q", block, err, want)
		}
	}
}

func TestPIIClassificationEndpointErrorsNeverEchoCredentials(t *testing.T) {
	_, err := loadPII(t, classifiers(entry(map[string]any{"endpoint": "http://user:hunter2-value@127.0.0.1:1"})))
	if err == nil || !strings.Contains(err.Error(), "userinfo") || strings.Contains(err.Error(), "hunter2-value") {
		t.Fatalf("err = %v", err)
	}
}
