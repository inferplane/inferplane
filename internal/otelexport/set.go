// Package otelexport is mayu's governed OTLP log export: audit-record copies
// and egress content, sent straight to collectors the operator declared in the
// local observability ceiling (config `observability.exporters`). Which
// subject's records go where is decided by GovernancePolicy observability
// rules; this package only enforces the ceiling (Allows) and moves bytes.
//
// Every path here is best-effort and bounded: enqueueing never blocks the
// request path or the audit writer, each exporter holds at most QueueBytes,
// and failures are counted in inferplane_otel_export_records_total. The
// hash-chained audit WAL remains the evidence of record. Traces are exported by
// internal/tracing; this package only reports which exporters allow them.
package otelexport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/inferplane/inferplane/internal/metrics"
)

// Signals an exporter may allow. They match the policy package's names.
const (
	SignalTraces  = "traces"
	SignalAudit   = "audit"
	SignalContent = "content"
)

// Exporter is one declared collector with its header values already resolved.
type Exporter struct {
	Name     string
	Endpoint string // host:port
	Protocol string // "http" (default) | "grpc"
	Insecure bool
	Headers  map[string]string
	Signals  []string
	// DataBoundary is the declared data boundary, shown by the console.
	DataBoundary string
	// MaxContentBytes caps one content record; QueueBytes caps the bytes this
	// exporter may hold. Zero values use the package defaults.
	MaxContentBytes int64
	QueueBytes      int64
}

// Defaults when an Exporter leaves a limit at zero (config validation sets the
// operator-facing defaults; these protect direct callers).
const (
	defaultMaxContentBytes = 2 << 20
	defaultQueueBytes      = 64 << 20
)

func (e Exporter) allows(signal string) bool {
	for _, s := range e.Signals {
		if s == signal {
			return true
		}
	}
	return false
}

// Set is the declared exporter ceiling plus one log pipeline per exporter that
// allows audit or content, and the runtime limits that narrow it.
type Set struct {
	exporters   map[string]*declared
	serviceName string
	st          *stats
	limits      atomic.Pointer[Limits] // runtime narrowing (limits.go); nil until SetLimits
}

type declared struct {
	cfg  Exporter
	logs *pipeline // nil when the exporter allows neither audit nor content
}

// New builds the set and starts a log pipeline for each exporter that allows a
// log signal. Collectors are connected lazily, so an unreachable collector at
// boot is not an error.
func New(serviceName string, exporters []Exporter, m *metrics.Metrics) (*Set, error) {
	if serviceName == "" {
		serviceName = "inferplane"
	}
	resource := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{stringAttr("service.name", serviceName)}}
	s := &Set{exporters: make(map[string]*declared, len(exporters)), serviceName: serviceName, st: newStats(m)}
	for _, e := range exporters {
		if e.Name == "" || e.Endpoint == "" {
			s.closeAll()
			return nil, errors.New("otelexport: exporter name and endpoint are required")
		}
		if _, dup := s.exporters[e.Name]; dup {
			s.closeAll()
			return nil, fmt.Errorf("otelexport: duplicate exporter %q", e.Name)
		}
		if e.MaxContentBytes <= 0 {
			e.MaxContentBytes = defaultMaxContentBytes
		}
		if e.QueueBytes <= 0 {
			e.QueueBytes = defaultQueueBytes
		}
		d := &declared{cfg: e}
		if e.allows(SignalAudit) || e.allows(SignalContent) {
			client, err := newLogClient(e)
			if err != nil {
				s.closeAll()
				return nil, fmt.Errorf("otelexport: exporter %q: %w", e.Name, err)
			}
			d.logs = newPipeline(e.Name, client, resource, e.QueueBytes, s.st, &s.limits)
		}
		s.exporters[e.Name] = d
	}
	return s, nil
}

// Allows is the policy ceiling check: nil when exporter is declared and allows
// signal. A nil Set allows nothing.
func (s *Set) Allows(exporter, signal string) error {
	if s == nil {
		return errors.New("no observability exporters are declared on this data plane")
	}
	d, ok := s.exporters[exporter]
	if !ok {
		return fmt.Errorf("exporter %q is not declared in this data plane's observability ceiling", exporter)
	}
	if !d.cfg.allows(signal) {
		return fmt.Errorf("exporter %q does not allow %s", exporter, signal)
	}
	return nil
}

// Has reports whether any declared exporter allows signal.
func (s *Set) Has(signal string) bool {
	if s == nil {
		return false
	}
	for _, d := range s.exporters {
		if d.cfg.allows(signal) {
			return true
		}
	}
	return false
}

// TraceExporters returns the exporters that allow traces, sorted by name.
func (s *Set) TraceExporters() []Exporter {
	if s == nil {
		return nil
	}
	var out []Exporter
	for _, d := range s.exporters {
		if d.cfg.allows(SignalTraces) {
			out = append(out, d.cfg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Shutdown flushes every pipeline until ctx ends. Records still queued when
// ctx expires are dropped and counted; the error reports that.
func (s *Set) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var errs []error
	for _, d := range s.exporters {
		if d.logs != nil {
			if err := d.logs.shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("exporter %q: %w", d.cfg.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Set) closeAll() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.Shutdown(ctx)
}

// emit enqueues one log record for exporter; it never blocks.
func (s *Set) emit(exporter, signal string, it item) {
	d := s.exporters[exporter]
	if d == nil || d.logs == nil || !d.cfg.allows(signal) {
		// Policy apply already checked the ceiling, so this is a programming
		// or reload-skew error: count it rather than export off-ceiling.
		if d == nil {
			// An undeclared name gets its Prometheus count only: it must not
			// create a stats key, which stays bounded by declared exporters.
			s.st.m.ObserveOTelExport(exporter, signal, outcomeExportError, 1)
			return
		}
		s.st.count(exporter, signal, outcomeExportError, 1)
		return
	}
	if !s.allowed(s.current(), exporter, signal) {
		// A limit installed after the decision: still never export.
		s.st.count(exporter, signal, outcomeWithheld, 1)
		return
	}
	it.signal = signal
	d.logs.enqueue(it)
}

func stringAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}
