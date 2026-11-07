package config

import "testing"

func TestMaskingIsExplicitAndExclusive(t *testing.T) {
	for _, raw := range []string{`{}`, `{"masking":{}}`, `{"masking":{"enabled":false}}`} {
		cfg, err := loadObservability(t, raw)
		if err != nil || cfg.Masking != nil && cfg.Masking.Enabled {
			t.Fatalf("masking defaulted on: %v", err)
		}
	}
	cfg, err := loadObservability(t, `{"masking":{"enabled":true,"teams":["private"],"state_file":"/tmp/masking.json"}}`)
	if err != nil || !cfg.Masking.Enabled || len(cfg.Masking.Teams) != 1 {
		t.Fatalf("explicit masking: %v", err)
	}
	if _, err := loadObservability(t, `{"masking":{},"plugins":[{"name":"pii-mask"}]}`); err == nil {
		t.Fatal("ambiguous masking controls accepted")
	}
}

func TestRequiredAuditExporterIsDeclaredAndAuditEnabled(t *testing.T) {
	const valid = `{"audit":{"sinks":[{"type":"otlp","exporter":"archive"}]},"observability":{"exporters":{"archive":{"endpoint":"collector.internal:4318","signals":["audit"]}}}}`
	if _, err := loadObservability(t, valid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"audit":{"sinks":[{"type":"otlp","exporter":"missing"}]}}`,
		`{"audit":{"sinks":[{"type":"file","path":"audit.jsonl","exporter":"archive"}]}}`,
		`{"audit":{"sinks":[{"type":"otlp","exporter":"archive"}]},"observability":{"exporters":{"archive":{"endpoint":"collector.internal:4318","signals":["traces"]}}}}`,
	} {
		if _, err := loadObservability(t, raw); err == nil {
			t.Fatalf("invalid required sink accepted: %s", raw)
		}
	}
}
