package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadJSON(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestClientEvidenceConfig(t *testing.T) {
	cfg, err := loadJSON(t, `{"audit":{"client_evidence":{"session_ref":"off","auto_mode":"on"}}}`)
	if err != nil || cfg.Audit.ClientEvidence == nil || cfg.Audit.ClientEvidence.SessionRef != "off" {
		t.Fatalf("parse: %+v %v", cfg, err)
	}
	for _, bad := range []string{
		`{"audit":{"client_evidence":{"session_ref":"raw"}}}`,
		`{"audit":{"client_evidence":{"auto_mode":"maybe"}}}`,
	} {
		if _, err := loadJSON(t, bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestTraceLinkTemplate(t *testing.T) {
	if _, err := loadJSON(t, `{"otel":{"endpoint":"c:4318","trace_link_template":"https://grafana.example/explore?traceId={trace_id}"}}`); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	for _, bad := range []string{
		`{"otel":{"endpoint":"c","trace_link_template":"https://grafana.example/explore"}}`,
		`{"otel":{"endpoint":"c","trace_link_template":"javascript:alert({trace_id})"}}`,
		`{"otel":{"endpoint":"c","trace_link_template":"ftp://host/{trace_id}"}}`,
		`{"otel":{"endpoint":"c","trace_link_template":"https:///{trace_id}"}}`,
	} {
		if _, err := loadJSON(t, bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
