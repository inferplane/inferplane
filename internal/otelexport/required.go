package otelexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// RequiredAuditSink is an explicit local audit destination, independent of
// subject-based best-effort copies. Its success is collector acceptance, never
// queue insertion. The audit writer owns the durable spool and its reclamation.
// There are no hidden retries: ambiguous delivery remains in the local WAL.
func (s *Set) RequiredAuditSink(name string) (audit.Sink, error) {
	if err := s.Allows(name, SignalAudit); err != nil {
		return nil, err
	}
	return &requiredSink{set: s, name: name, client: s.exporters[name].logs.client}, nil
}

type requiredSink struct {
	set    *Set
	name   string
	client logClient
}

func (s *requiredSink) Name() string            { return "otlp" }
func (s *requiredSink) Required() bool          { return true }
func (s *requiredSink) Close() error            { return nil } // owned by Set.Shutdown, after writer drain
func (s *requiredSink) Write(line []byte) error { return s.WriteBatch([][]byte{line}) }
func (s *requiredSink) WriteBatch(lines [][]byte) error {
	if !s.set.allowed(s.set.current(), s.name, SignalAudit) {
		return errors.New("required audit exporter is paused or narrowed")
	}
	// Bound transport batches separately from the writer's group commit. A
	// failure in any sub-batch retains the entire original group.
	for len(lines) > 0 {
		n, size := 0, 0
		var records []*logspb.LogRecord
		for n < len(lines) && (n == 0 || size+len(lines[n])+recordOverhead <= maxBatchBytes) {
			line := lines[n]
			sum := sha256.Sum256(line)
			records = append(records, &logspb.LogRecord{
				ObservedTimeUnixNano: uint64(time.Now().UnixNano()),
				EventName:            "inferplane.audit",
				Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: string(line)}},
				Attributes:           []*commonpb.KeyValue{stringAttr("inferplane.audit.hash", "sha256:"+hex.EncodeToString(sum[:]))},
			})
			size += len(line) + recordOverhead
			n++
		}
		req := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
			Resource:  &resourcepb.Resource{Attributes: []*commonpb.KeyValue{stringAttr("service.name", s.set.serviceName)}},
			ScopeLogs: []*logspb.ScopeLogs{{Scope: &commonpb.InstrumentationScope{Name: scopeName}, LogRecords: records}},
		}}}
		ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
		err := s.client.export(ctx, req)
		cancel()
		if err != nil {
			s.set.st.failed(s.name, SignalAudit, err.Error())
			return err
		}
		s.set.st.count(s.name, SignalAudit, outcomeSent, n)
		s.set.st.delivered(s.name, SignalAudit)
		lines = lines[n:]
	}
	return nil
}
