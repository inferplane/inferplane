package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadObservability(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestObservabilityCeilingParses(t *testing.T) {
	cfg, err := loadObservability(t, `{"observability":{"service_name":"mayu-dev","exporters":{
		"corp":{"endpoint":"otel.internal:4318","signals":["traces","audit"],"headers":{"authorization":{"env":"OTEL_AUTH"}}},
		"vault":{"endpoint":"vault.internal:4317","protocol":"grpc","signals":["content"],"data_boundary":"internal","max_content_bytes":1048576,"queue_bytes":16777216}
	}}}`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	o := cfg.Observability
	if o == nil || o.ServiceName != "mayu-dev" || len(o.Exporters) != 2 {
		t.Fatalf("parsed = %+v", o)
	}
	corp := o.Exporters["corp"]
	if corp.Endpoint != "otel.internal:4318" || corp.Headers["authorization"].Env != "OTEL_AUTH" || strings.Join(corp.Signals, ",") != "traces,audit" {
		t.Fatalf("corp = %+v", corp)
	}
	if got := corp.ContentLimit(); got != DefaultMaxContentBytes {
		t.Fatalf("default content limit = %d", got)
	}
	if got := corp.QueueLimit(); got != DefaultExportQueueBytes {
		t.Fatalf("default queue limit = %d", got)
	}
	if got := o.Exporters["vault"].QueueLimit(); got != 16<<20 {
		t.Fatalf("explicit queue limit = %d", got)
	}
}

func TestObservabilityCeilingRejects(t *testing.T) {
	cases := []struct {
		name, exporters, want string
	}{
		{"empty map", `{}`, "at least one exporter"},
		{"bad name", `{"Corp":{"endpoint":"h:1","signals":["traces"]}}`, "exporter name"},
		{"reserved otel", `{"otel":{"endpoint":"h:1","signals":["traces"]}}`, "reserved"},
		{"scheme in endpoint", `{"corp":{"endpoint":"https://h:4318","signals":["traces"]}}`, "host:port"},
		{"path in endpoint", `{"corp":{"endpoint":"h:4318/v1","signals":["traces"]}}`, "host:port"},
		{"missing port", `{"corp":{"endpoint":"collector","signals":["traces"]}}`, "host:port"},
		{"protocol", `{"corp":{"endpoint":"h:1","protocol":"udp","signals":["traces"]}}`, "protocol"},
		{"no signals", `{"corp":{"endpoint":"h:1","signals":[]}}`, "signals"},
		{"unknown signal", `{"corp":{"endpoint":"h:1","signals":["metrics"]}}`, "signal"},
		{"duplicate signal", `{"corp":{"endpoint":"h:1","signals":["audit","audit"]}}`, "duplicate"},
		{"content without boundary", `{"corp":{"endpoint":"h:1","signals":["content"]}}`, "data_boundary"},
		{"content external", `{"corp":{"endpoint":"h:1","signals":["content"],"data_boundary":"external"}}`, "data_boundary"},
		{"content plaintext remote", `{"corp":{"endpoint":"collector.internal:4318","insecure":true,"signals":["content"],"data_boundary":"internal"}}`, "TLS"},
		{"header both refs", `{"corp":{"endpoint":"h:1","signals":["traces"],"headers":{"authorization":{"env":"A","file":"/x"}}}}`, "header"},
		{"header no ref", `{"corp":{"endpoint":"h:1","signals":["traces"],"headers":{"authorization":{}}}}`, "header"},
		{"header name", `{"corp":{"endpoint":"h:1","signals":["traces"],"headers":{"bad header":{"env":"A"}}}}`, "header name"},
		{"content limit too big", `{"corp":{"endpoint":"h:1","signals":["content"],"data_boundary":"internal","max_content_bytes":16777217}}`, "max_content_bytes"},
		{"queue too small", `{"corp":{"endpoint":"h:1","signals":["audit"],"queue_bytes":1024}}`, "queue_bytes"},
		{"queue too big", `{"corp":{"endpoint":"h:1","signals":["audit"],"queue_bytes":1073741825}}`, "queue_bytes"},
		{"queue below content", `{"corp":{"endpoint":"h:1","signals":["content"],"data_boundary":"internal","max_content_bytes":4194304,"queue_bytes":2097152}}`, "queue_bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadObservability(t, `{"observability":{"exporters":`+tc.exporters+`}}`)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestObservabilityContentAllowsLoopbackPlaintext(t *testing.T) {
	for _, ep := range []string{"127.0.0.1:4318", "localhost:4318", "[::1]:4317"} {
		if _, err := loadObservability(t, `{"observability":{"exporters":{"local":{"endpoint":"`+ep+`","insecure":true,"signals":["content"],"data_boundary":"internal"}}}}`); err != nil {
			t.Fatalf("%s: %v", ep, err)
		}
	}
}

func TestObservabilityHeaderErrorNeverEchoesRef(t *testing.T) {
	_, err := loadObservability(t, `{"observability":{"exporters":{"corp":{"endpoint":"h:1","signals":["traces"],"headers":{"authorization":{"env":"Bearer sk-live-value"}}}}}}`)
	if err == nil || strings.Contains(err.Error(), "sk-live-value") {
		t.Fatalf("err = %v: a malformed ref must be refused without echoing it", err)
	}
}

// The example config's commented-out block must be a valid ceiling once renamed.
func TestObservabilityExampleBlockValidates(t *testing.T) {
	raw, err := os.ReadFile("../../examples/config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	block, ok := doc["_observability_example"]
	if !ok {
		t.Fatal("examples/config.json lost _observability_example")
	}
	if _, err := loadObservability(t, `{"observability":`+string(block)+`}`); err != nil {
		t.Fatalf("example block: %v", err)
	}
}
