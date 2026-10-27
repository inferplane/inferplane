package otelexport

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"

	"github.com/inferplane/inferplane/internal/audit"
)

// recordOverhead approximates per-record attribute bytes for the queue budget.
const recordOverhead = 512

// AuditSink returns a non-required audit sink that copies each record to the
// exporters decide selects for its (team, account) subject. decide must be
// cheap and non-blocking: it runs on the audit writer's serialized loop.
func (s *Set) AuditSink(decide func(team, user string) []string) audit.Sink {
	return &auditSink{set: s, decide: decide}
}

type auditSink struct {
	set    *Set
	decide func(team, user string) []string
}

func (a *auditSink) Name() string   { return "otlp" }
func (a *auditSink) Required() bool { return false }
func (a *auditSink) Close() error   { return nil } // Set.Shutdown flushes

// Write is unused: the writer calls WriteRecord on a RecordSink.
func (a *auditSink) Write([]byte) error { return nil }

func (a *auditSink) WriteRecord(rec *audit.Record, canon []byte) error {
	user := rec.Principal.Account
	if user == "" && rec.Principal.User != nil {
		user = *rec.Principal.User
	}
	targets := a.decide(rec.Principal.Team, user)
	if len(targets) == 0 {
		return nil
	}
	sum := sha256.Sum256(canon)
	lr := &logspb.LogRecord{
		TimeUnixNano:         recordTime(rec.TS),
		ObservedTimeUnixNano: uint64(time.Now().UnixNano()),
		SeverityNumber:       logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
		SeverityText:         "INFO",
		EventName:            "inferplane.audit",
		Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: string(canon)}},
		Attributes: []*commonpb.KeyValue{
			stringAttr("inferplane.audit.event", rec.Event),
			stringAttr("inferplane.audit.id", rec.ID),
			stringAttr("inferplane.audit.instance", rec.Instance),
			// hash is the next record's prev_hash: a receiver can prove gaps.
			stringAttr("inferplane.audit.hash", "sha256:"+hex.EncodeToString(sum[:])),
			stringAttr("inferplane.audit.prev_hash", rec.PrevHash),
			stringAttr("inferplane.team", rec.Principal.Team),
		},
	}
	if rec.TraceID != nil {
		lr.TraceId = traceID(*rec.TraceID)
	}
	size := int64(len(canon)) + recordOverhead
	for i, name := range targets {
		r := lr
		if i > 0 {
			r = proto.Clone(lr).(*logspb.LogRecord) // pipelines marshal concurrently
		}
		a.set.emit(name, SignalAudit, item{rec: r, size: size})
	}
	return nil
}

// Content is one request's egress bytes: the provider request after routing
// and masking, and the buffered response when there is one (nil for streams).
type Content struct {
	RecordID string // the audit record id, so content joins its audit record
	Team     string
	Ingress  string
	Model    string // requested (canonical) model
	Upstream string // routed upstream model id
	TraceID  string
	Request  []byte
	Response []byte
}

// EmitContent enqueues c for exporter. Records over the exporter's
// MaxContentBytes are dropped and counted, never truncated.
func (s *Set) EmitContent(exporter string, c Content) {
	if s == nil {
		return
	}
	d := s.exporters[exporter]
	size := int64(len(c.Request) + len(c.Response))
	if d != nil && size > d.cfg.MaxContentBytes {
		s.m.ObserveOTelExport(exporter, SignalContent, outcomeOversize, 1)
		return
	}
	values := []*commonpb.KeyValue{stringAttr("request", string(c.Request))}
	if c.Response != nil {
		values = append(values, stringAttr("response", string(c.Response)))
	}
	now := uint64(time.Now().UnixNano())
	lr := &logspb.LogRecord{
		TimeUnixNano:         now,
		ObservedTimeUnixNano: now,
		SeverityNumber:       logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
		SeverityText:         "INFO",
		EventName:            "inferplane.content",
		Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_KvlistValue{KvlistValue: &commonpb.KeyValueList{Values: values}}},
		Attributes: []*commonpb.KeyValue{
			stringAttr("inferplane.audit.id", c.RecordID),
			stringAttr("inferplane.team", c.Team),
			stringAttr("inferplane.ingress", c.Ingress),
			stringAttr("gen_ai.request.model", c.Model),
			stringAttr("gen_ai.response.model", c.Upstream),
		},
	}
	if c.TraceID != "" {
		lr.TraceId = traceID(c.TraceID)
	}
	s.emit(exporter, SignalContent, item{rec: lr, size: size + recordOverhead})
}

func recordTime(ts string) uint64 {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return uint64(t.UnixNano())
	}
	return uint64(time.Now().UnixNano())
}

// traceID decodes a 32-hex trace id; anything else yields no trace id.
func traceID(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return nil
	}
	return b
}
