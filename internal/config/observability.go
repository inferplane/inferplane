package config

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/inferplane/inferplane/internal/policy"
)

// Export limits for the observability ceiling.
const (
	// DefaultMaxContentBytes bounds one exported content record (request plus
	// response). Larger records are dropped and counted, never truncated: a
	// truncated JSON body is not evidence of anything.
	DefaultMaxContentBytes int64 = 2 << 20
	maxContentBytesLimit   int64 = 16 << 20
	// DefaultExportQueueBytes bounds the memory one exporter may hold while
	// its collector is slow or down.
	DefaultExportQueueBytes int64 = 64 << 20
	minExportQueueBytes     int64 = 1 << 20
	maxExportQueueBytes     int64 = 1 << 30
)

// headerName is a lowercase HTTP field name, which is also a valid gRPC
// metadata key.
var headerName = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$%&'*+.^_|~-]*$`)

// ObservabilityConfig is the local export ceiling: every OTLP destination a
// GovernancePolicy observability rule may select. Policies choose among these
// exporters by name; endpoints, credentials, TLS and the signals an exporter
// may carry are set here only, so a policy can never add a destination.
type ObservabilityConfig struct {
	ServiceName string                           `json:"service_name,omitempty"`
	Exporters   map[string]ObservabilityExporter `json:"exporters"`
}

// ObservabilityExporter declares one OTLP collector. Endpoint is host:port;
// HTTP sends /v1/traces and /v1/logs on it. Headers resolve from env/file
// refs (for example a collector bearer token).
type ObservabilityExporter struct {
	Endpoint string                `json:"endpoint"`
	Protocol string                `json:"protocol,omitempty"` // "http" (default) | "grpc"
	Insecure bool                  `json:"insecure,omitempty"`
	Headers  map[string]*SecretRef `json:"headers,omitempty"`
	// Signals lists what policies may send here: traces, audit, content.
	Signals []string `json:"signals"`
	// DataBoundary must be "internal" for an exporter that allows content:
	// it receives request bodies, the same trust a provider needs to receive
	// InternalOnly traffic. Empty/unknown never grants it.
	DataBoundary    string `json:"data_boundary,omitempty"`
	MaxContentBytes int64  `json:"max_content_bytes,omitempty"`
	QueueBytes      int64  `json:"queue_bytes,omitempty"`
}

// Allows reports whether this exporter may carry signal.
func (e ObservabilityExporter) Allows(signal string) bool {
	for _, s := range e.Signals {
		if s == signal {
			return true
		}
	}
	return false
}

// ContentLimit returns the effective per-record content cap.
func (e ObservabilityExporter) ContentLimit() int64 {
	if e.MaxContentBytes > 0 {
		return e.MaxContentBytes
	}
	return DefaultMaxContentBytes
}

// QueueLimit returns the effective queued-bytes cap.
func (e ObservabilityExporter) QueueLimit() int64 {
	if e.QueueBytes > 0 {
		return e.QueueBytes
	}
	return DefaultExportQueueBytes
}

// ReservedLegacyExporter is the name the legacy `otel` block occupies.
const ReservedLegacyExporter = "otel"

func validateObservability(o *ObservabilityConfig) error {
	if o == nil {
		return nil
	}
	if len(o.Exporters) == 0 {
		return fmt.Errorf("config: observability.exporters must declare at least one exporter when the observability block is present")
	}
	names := make([]string, 0, len(o.Exporters))
	for name := range o.Exporters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := validateExporter(name, o.Exporters[name]); err != nil {
			return fmt.Errorf("config: observability.exporters[%q]: %w", name, err)
		}
	}
	return nil
}

func validateExporter(name string, e ObservabilityExporter) error {
	if !policy.ValidExporterName(name) {
		return fmt.Errorf("exporter name must be lowercase letters, digits and '-' (at most 63)")
	}
	if name == ReservedLegacyExporter {
		return fmt.Errorf("the name %q is reserved for the legacy otel block", name)
	}
	host, err := endpointHost(e.Endpoint)
	if err != nil {
		return err
	}
	switch e.Protocol {
	case "", "http", "grpc":
	default:
		return fmt.Errorf("protocol must be \"http\" or \"grpc\", got %q", e.Protocol)
	}
	if len(e.Signals) == 0 {
		return fmt.Errorf("signals must list at least one of traces, audit, content")
	}
	seen := map[string]bool{}
	for _, s := range e.Signals {
		switch s {
		case policy.SignalTraces, policy.SignalAudit, policy.SignalContent:
		default:
			return fmt.Errorf("unknown signal %q (supported: traces, audit, content)", s)
		}
		if seen[s] {
			return fmt.Errorf("duplicate signal %q", s)
		}
		seen[s] = true
	}
	if err := ValidateDataBoundary(e.DataBoundary); err != nil {
		return err
	}
	if seen[policy.SignalContent] {
		if e.DataBoundary != "internal" {
			return fmt.Errorf("an exporter that allows content must declare data_boundary \"internal\"")
		}
		if e.Insecure && !loopbackHost(host) {
			return fmt.Errorf("an exporter that allows content must use TLS unless its endpoint is loopback")
		}
	}
	for hn, ref := range e.Headers {
		if !headerName.MatchString(hn) {
			return fmt.Errorf("header name %q must be a lowercase HTTP field name", hn)
		}
		if ref == nil {
			return fmt.Errorf("header %q: secret ref must set env or file", hn)
		}
		if err := ValidateSecretRef(ref); err != nil {
			return fmt.Errorf("header %q: %w", hn, err)
		}
	}
	if e.MaxContentBytes < 0 || e.MaxContentBytes > maxContentBytesLimit {
		return fmt.Errorf("max_content_bytes must be in [0, %d] (0 = default %d)", maxContentBytesLimit, DefaultMaxContentBytes)
	}
	if e.QueueBytes != 0 && (e.QueueBytes < minExportQueueBytes || e.QueueBytes > maxExportQueueBytes) {
		return fmt.Errorf("queue_bytes must be 0 (default %d) or in [%d, %d]", DefaultExportQueueBytes, minExportQueueBytes, maxExportQueueBytes)
	}
	if seen[policy.SignalContent] && e.QueueLimit() < e.ContentLimit() {
		return fmt.Errorf("queue_bytes (%d) must be at least max_content_bytes (%d)", e.QueueLimit(), e.ContentLimit())
	}
	return nil
}

// endpointHost validates a bare host:port and returns the host.
func endpointHost(ep string) (string, error) {
	if ep == "" {
		return "", fmt.Errorf("endpoint is required")
	}
	if strings.Contains(ep, "://") || strings.ContainsAny(ep, "/?#") {
		return "", fmt.Errorf("endpoint must be a bare host:port (no scheme or path)")
	}
	host, port, err := net.SplitHostPort(ep)
	if err != nil || host == "" {
		return "", fmt.Errorf("endpoint must be a bare host:port (no scheme or path)")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("endpoint must be a bare host:port with a port in 1..65535")
	}
	return host, nil
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
