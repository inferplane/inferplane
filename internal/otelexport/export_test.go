package otelexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/metrics"
)

// collector is a fake OTLP/HTTP logs receiver.
type collector struct {
	mu       sync.Mutex
	srv      *httptest.Server
	requests []*collogspb.ExportLogsServiceRequest
	headers  []http.Header
	statuses []int // served in order; 200 once exhausted
	block    chan struct{}
	arrived  chan struct{} // when set, signalled (without waiting) as each request arrives
}

func newCollector(t *testing.T, statuses ...int) *collector {
	t.Helper()
	c := &collector{statuses: statuses}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.arrived != nil {
			select {
			case c.arrived <- struct{}{}:
			default:
			}
		}
		if c.block != nil {
			select {
			case <-c.block:
			case <-r.Context().Done():
				return
			}
		}
		body, _ := io.ReadAll(r.Body)
		req := &collogspb.ExportLogsServiceRequest{}
		if r.URL.Path != "/v1/logs" || r.Header.Get("Content-Type") != "application/x-protobuf" || proto.Unmarshal(body, req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		status := http.StatusOK
		if len(c.statuses) > 0 {
			status, c.statuses = c.statuses[0], c.statuses[1:]
		}
		if status == http.StatusOK {
			c.requests = append(c.requests, req)
			c.headers = append(c.headers, r.Header.Clone())
		}
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(func() {
		if c.block != nil {
			select {
			case <-c.block:
			default:
				close(c.block)
			}
		}
		c.srv.Close()
	})
	return c
}

func (c *collector) endpoint() string { return strings.TrimPrefix(c.srv.URL, "http://") }

func (c *collector) records() []*logspb.LogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*logspb.LogRecord
	for _, r := range c.requests {
		for _, rl := range r.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				out = append(out, sl.LogRecords...)
			}
		}
	}
	return out
}

func attr(lr *logspb.LogRecord, key string) string {
	for _, kv := range lr.Attributes {
		if kv.Key == key {
			return kv.Value.GetStringValue()
		}
	}
	return ""
}

func fastRetry(t *testing.T) {
	t.Helper()
	old := retryBackoff
	retryBackoff = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	t.Cleanup(func() { retryBackoff = old })
}

func newTestSet(t *testing.T, m *metrics.Metrics, exporters ...Exporter) *Set {
	t.Helper()
	s, err := New("mayu-test", exporters, m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func shutdown(t *testing.T, s *Set) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func counter(m *metrics.Metrics, exporter, signal, outcome string) float64 {
	families, err := m.Registry().Gather()
	if err != nil {
		panic(err)
	}
	for _, f := range families {
		if f.GetName() != "inferplane_otel_export_records_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range metric.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["exporter"] == exporter && labels["signal"] == signal && labels["outcome"] == outcome {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func auditRecord() (*audit.Record, []byte) {
	trace := "0af7651916cd43dd8448eb211c80319c"
	prev := "sha256:abc"
	rec := &audit.Record{SchemaVersion: 1, Event: "request_completed", ID: "01JREC", TS: "2026-10-27T10:00:00.5Z",
		Instance: "node-1", PrevHash: prev, TraceID: &trace,
		Principal: audit.PrincipalRef{KeyID: "ik_abc", Team: "eng", Account: "alice"}}
	canon, _ := rec.Canonical()
	return rec, canon
}

func TestAuditRecordExported(t *testing.T) {
	col := newCollector(t)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true,
		Headers: map[string]string{"authorization": "Bearer collector-token"}, Signals: []string{"traces", "audit"}})
	var gotTeam, gotUser string
	sink := s.AuditSink(func(team, user string) []string { gotTeam, gotUser = team, user; return []string{"corp"} })
	if sink.Required() {
		t.Fatal("the OTLP audit copy must never be a required sink")
	}
	rs, ok := sink.(audit.RecordSink)
	if !ok {
		t.Fatal("audit sink must implement audit.RecordSink")
	}
	rec, canon := auditRecord()
	if err := rs.WriteRecord(rec, canon); err != nil {
		t.Fatal(err)
	}
	shutdown(t, s)

	if gotTeam != "eng" || gotUser != "alice" {
		t.Fatalf("decision subject = %q/%q, want the record's team and account", gotTeam, gotUser)
	}
	recs := col.records()
	if len(recs) != 1 {
		t.Fatalf("collector got %d records", len(recs))
	}
	lr := recs[0]
	sum := sha256.Sum256(canon)
	if lr.Body.GetStringValue() != string(canon) || lr.EventName != "inferplane.audit" ||
		attr(lr, "inferplane.audit.hash") != "sha256:"+hex.EncodeToString(sum[:]) ||
		attr(lr, "inferplane.audit.prev_hash") != "sha256:abc" || attr(lr, "inferplane.audit.id") != "01JREC" ||
		attr(lr, "inferplane.audit.event") != "request_completed" || attr(lr, "inferplane.audit.instance") != "node-1" ||
		attr(lr, "inferplane.team") != "eng" {
		t.Fatalf("audit log record = %v", lr)
	}
	if hex.EncodeToString(lr.TraceId) != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("trace id = %x", lr.TraceId)
	}
	if lr.TimeUnixNano != uint64(time.Date(2026, 10, 27, 10, 0, 0, 5e8, time.UTC).UnixNano()) {
		t.Fatalf("timestamp = %d", lr.TimeUnixNano)
	}
	if got := col.headers[0].Get("Authorization"); got != "Bearer collector-token" {
		t.Fatalf("collector header = %q", got)
	}
	res := col.requests[0].ResourceLogs[0].Resource
	if res.Attributes[0].Key != "service.name" || res.Attributes[0].Value.GetStringValue() != "mayu-test" {
		t.Fatalf("resource = %v", res)
	}
	if strings.Contains(fmt.Sprint(lr.Attributes), "alice") {
		t.Fatal("the raw account must not become a log attribute")
	}
	if counter(m, "corp", "audit", "sent") != 1 {
		t.Fatalf("sent counter = %v", counter(m, "corp", "audit", "sent"))
	}
}

func TestAuditSinkSkipsWithoutTargets(t *testing.T) {
	col := newCollector(t)
	s := newTestSet(t, metrics.New(), Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}})
	rec, canon := auditRecord()
	if err := s.AuditSink(func(string, string) []string { return nil }).(audit.RecordSink).WriteRecord(rec, canon); err != nil {
		t.Fatal(err)
	}
	shutdown(t, s)
	if n := len(col.records()); n != 0 {
		t.Fatalf("exported %d records with no governed target", n)
	}
}

func TestContentExported(t *testing.T) {
	col := newCollector(t)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "vault", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"content"}, MaxContentBytes: 1 << 20})
	s.EmitContent("vault", Content{RecordID: "01JREC", Team: "eng", Ingress: "anthropic", Model: "sonnet", Upstream: "global.anthropic.claude-sonnet-5-5",
		TraceID: "0af7651916cd43dd8448eb211c80319c", Request: []byte(`{"messages":[]}`), Response: []byte(`{"content":[]}`)})
	shutdown(t, s)
	recs := col.records()
	if len(recs) != 1 {
		t.Fatalf("collector got %d records", len(recs))
	}
	lr := recs[0]
	body := map[string]string{}
	for _, kv := range lr.Body.GetKvlistValue().GetValues() {
		body[kv.Key] = kv.Value.GetStringValue()
	}
	if body["request"] != `{"messages":[]}` || body["response"] != `{"content":[]}` || lr.EventName != "inferplane.content" ||
		attr(lr, "inferplane.audit.id") != "01JREC" || attr(lr, "gen_ai.request.model") != "sonnet" ||
		attr(lr, "gen_ai.response.model") != "global.anthropic.claude-sonnet-5-5" || attr(lr, "inferplane.ingress") != "anthropic" {
		t.Fatalf("content record = %v", lr)
	}
	if counter(m, "vault", "content", "sent") != 1 {
		t.Fatal("content sent counter not incremented")
	}
}

func TestContentWithoutResponseOmitsField(t *testing.T) {
	col := newCollector(t)
	s := newTestSet(t, metrics.New(), Exporter{Name: "vault", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"content"}, MaxContentBytes: 1 << 20})
	s.EmitContent("vault", Content{RecordID: "r", Request: []byte(`{}`)})
	shutdown(t, s)
	for _, kv := range col.records()[0].Body.GetKvlistValue().GetValues() {
		if kv.Key == "response" {
			t.Fatal("a streaming request has no buffered response; the field must be absent, not empty")
		}
	}
}

func TestContentOversizeDropped(t *testing.T) {
	col := newCollector(t)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "vault", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"content"}, MaxContentBytes: 10})
	s.EmitContent("vault", Content{RecordID: "r", Request: []byte(strings.Repeat("x", 20))})
	shutdown(t, s)
	if n := len(col.records()); n != 0 {
		t.Fatalf("oversize content exported: %d records", n)
	}
	if counter(m, "vault", "content", "dropped_oversize") != 1 {
		t.Fatal("oversize drop not counted")
	}
}

func TestRetryOnUnavailable(t *testing.T) {
	fastRetry(t)
	col := newCollector(t, http.StatusServiceUnavailable)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}})
	rec, canon := auditRecord()
	s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink).WriteRecord(rec, canon)
	shutdown(t, s)
	if n := len(col.records()); n != 1 {
		t.Fatalf("a 503 must be retried: got %d records", n)
	}
	if counter(m, "corp", "audit", "sent") != 1 || counter(m, "corp", "audit", "dropped_export_error") != 0 {
		t.Fatal("retry outcome miscounted")
	}
}

func TestPermanentErrorDropped(t *testing.T) {
	fastRetry(t)
	col := newCollector(t, http.StatusBadRequest)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}})
	rec, canon := auditRecord()
	s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink).WriteRecord(rec, canon)
	shutdown(t, s)
	if n := len(col.records()); n != 0 {
		t.Fatalf("a 400 must not be retried: got %d records", n)
	}
	if counter(m, "corp", "audit", "dropped_export_error") != 1 {
		t.Fatal("permanent failure not counted")
	}
}

func TestQueueDropsWhenFullWithoutBlocking(t *testing.T) {
	col := newCollector(t)
	col.block = make(chan struct{})
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "vault", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"content"},
		MaxContentBytes: 64 << 10, QueueBytes: 256 << 10})
	payload := []byte(strings.Repeat("x", 60<<10))
	for i := 0; i < 50; i++ {
		start := time.Now()
		s.EmitContent("vault", Content{RecordID: fmt.Sprint(i), Request: payload})
		if d := time.Since(start); d > 50*time.Millisecond {
			t.Fatalf("enqueue %d blocked for %v", i, d)
		}
	}
	if counter(m, "vault", "content", "dropped_queue_full") == 0 {
		t.Fatal("a full queue must drop and count, not grow")
	}
	close(col.block)
	shutdown(t, s)
}

func TestShutdownHonorsDeadline(t *testing.T) {
	col := newCollector(t)
	col.block = make(chan struct{})
	s := newTestSet(t, metrics.New(), Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}})
	rec, canon := auditRecord()
	s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink).WriteRecord(rec, canon)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := s.Shutdown(ctx)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Shutdown ignored its deadline: %v", d)
	}
	if err == nil {
		t.Fatal("Shutdown must report the records it could not flush")
	}
}

func TestEmitAfterShutdownIsDropped(t *testing.T) {
	col := newCollector(t)
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "vault", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"content"}, MaxContentBytes: 1 << 20})
	shutdown(t, s)
	s.EmitContent("vault", Content{RecordID: "late", Request: []byte(`{}`)})
	if counter(m, "vault", "content", "dropped_queue_full") != 1 {
		t.Fatal("a record after shutdown must be dropped and counted, never panic")
	}
}

// nonBlockingDeadline only detects a hang. The check below never compares
// timings, so a slow or loaded host cannot fail it; only a sink that waits for
// its collector can.
const nonBlockingDeadline = 30 * time.Second

// writeAll appends n records through an audit writer whose only sink is sink,
// then closes it. Close returns only after the writer goroutine has handed
// every record to the sink, so a value on the returned channel proves that all
// n writes completed. The channel yields Close's error.
func writeAll(t *testing.T, sink audit.Sink, n int) <-chan error {
	t.Helper()
	w, err := audit.NewWriter("node-1", t.TempDir()+"/wal", []audit.Sink{sink})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			w.Append(audit.Record{SchemaVersion: 1, Event: "request_completed", ID: fmt.Sprint(i), Principal: audit.PrincipalRef{Team: "eng"}})
		}
		done <- w.Close()
	}()
	return done
}

// The audit writer hands each record to every sink on its single goroutine, so
// an OTLP copy that waited for its collector would stall the whole chain. The
// collector stays blocked until the writer has written and closed all n
// records; only then is it released.
func TestAuditSinkNeverBlocksWriter(t *testing.T) {
	const n = 400
	col := newCollector(t)
	col.block = make(chan struct{})
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}, QueueBytes: 64 << 10})
	done := writeAll(t, s.AuditSink(func(string, string) []string { return []string{"corp"} }), n)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(nonBlockingDeadline):
		t.Fatalf("the audit writer was still writing after %v with the collector blocked: the OTLP sink waits for its collector", nonBlockingDeadline)
	}
	if got := len(col.records()); got != 0 {
		t.Fatalf("the collector accepted %d records before it was released; the test did not hold it blocked", got)
	}
	if counter(m, "corp", "audit", "dropped_queue_full") == 0 {
		t.Fatal("the byte budget must drop once the blocked collector backs up")
	}
	close(col.block)
	shutdown(t, s)
}

// blockingSink waits on release for every record: the kind of sink
// TestAuditSinkNeverBlocksWriter exists to catch.
type blockingSink struct{ release chan struct{} }

func (b blockingSink) Write([]byte) error { <-b.release; return nil }
func (b blockingSink) Name() string       { return "blocking" }
func (b blockingSink) Required() bool     { return false }
func (b blockingSink) Close() error       { return nil }

// RED evidence for the check above, kept in the suite: the same drain check
// does not complete while a sink waits on something the test holds.
func TestNonBlockingCheckCatchesABlockingSink(t *testing.T) {
	sink := blockingSink{release: make(chan struct{})}
	done := writeAll(t, sink, 5)
	select {
	case <-done:
		t.Fatal("a blocking sink drained while it was held: the drain check would pass a sink that waits")
	case <-time.After(250 * time.Millisecond):
	}
	close(sink.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAllows(t *testing.T) {
	s := newTestSet(t, metrics.New(),
		Exporter{Name: "corp", Endpoint: "127.0.0.1:1", Insecure: true, Signals: []string{"traces", "audit"}},
		Exporter{Name: "vault", Endpoint: "127.0.0.1:2", Insecure: true, Signals: []string{"content"}})
	defer shutdown(t, s)
	for _, tc := range []struct {
		exporter, signal string
		ok               bool
	}{{"corp", "traces", true}, {"corp", "audit", true}, {"corp", "content", false}, {"vault", "content", true}, {"vault", "traces", false}, {"missing", "audit", false}} {
		err := s.Allows(tc.exporter, tc.signal)
		if (err == nil) != tc.ok {
			t.Errorf("Allows(%s,%s) = %v", tc.exporter, tc.signal, err)
		}
	}
	if !s.Has("content") || !s.Has("traces") {
		t.Fatal("Has must report declared signals")
	}
	var none *Set
	if none.Allows("corp", "traces") == nil || none.Has("traces") {
		t.Fatal("a nil set allows nothing")
	}
	if got := s.TraceExporters(); len(got) != 1 || got[0].Name != "corp" {
		t.Fatalf("TraceExporters = %+v", got)
	}
}

func TestNewRejectsDuplicateNames(t *testing.T) {
	_, err := New("x", []Exporter{{Name: "a", Endpoint: "h:1", Signals: []string{"audit"}}, {Name: "a", Endpoint: "h:2", Signals: []string{"audit"}}}, nil)
	if err == nil {
		t.Fatal("duplicate exporter names must be refused")
	}
}

type grpcLogs struct {
	collogspb.UnimplementedLogsServiceServer
	mu   sync.Mutex
	got  []*collogspb.ExportLogsServiceRequest
	auth []string
}

func (g *grpcLogs) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	g.mu.Lock()
	g.got = append(g.got, req)
	g.auth = append(g.auth, md.Get("authorization")...)
	g.mu.Unlock()
	return &collogspb.ExportLogsServiceResponse{}, nil
}

func TestGRPCExport(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	logs := &grpcLogs{}
	collogspb.RegisterLogsServiceServer(srv, logs)
	go srv.Serve(lis)
	defer srv.Stop()
	s := newTestSet(t, metrics.New(), Exporter{Name: "corp", Endpoint: lis.Addr().String(), Protocol: "grpc", Insecure: true,
		Headers: map[string]string{"authorization": "Bearer grpc-token"}, Signals: []string{"audit"}})
	rec, canon := auditRecord()
	s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink).WriteRecord(rec, canon)
	shutdown(t, s)
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if len(logs.got) != 1 || logs.got[0].ResourceLogs[0].ScopeLogs[0].LogRecords[0].Body.GetStringValue() != string(canon) {
		t.Fatalf("gRPC collector got %v", logs.got)
	}
	if len(logs.auth) != 1 || logs.auth[0] != "Bearer grpc-token" {
		t.Fatalf("gRPC metadata = %v", logs.auth)
	}
}

// queue_bytes is the documented bound: small audit copies must not hit a
// fixed item cap long before the byte budget is used.
func TestQueueBoundIsBytesNotItems(t *testing.T) {
	col := newCollector(t)
	col.block = make(chan struct{})
	m := metrics.New()
	s := newTestSet(t, m, Exporter{Name: "corp", Endpoint: col.endpoint(), Insecure: true, Signals: []string{"audit"}, QueueBytes: 64 << 20})
	sink := s.AuditSink(func(string, string) []string { return []string{"corp"} }).(audit.RecordSink)
	rec, canon := auditRecord()
	for i := 0; i < 12000; i++ { // ~12000 × (len(canon)+512) ≈ 9 MiB, well under 64 MiB
		sink.WriteRecord(rec, canon)
	}
	if dropped := counter(m, "corp", "audit", "dropped_queue_full"); dropped != 0 {
		t.Fatalf("dropped %v records with the byte budget mostly unused", dropped)
	}
	close(col.block)
	shutdown(t, s)
}
