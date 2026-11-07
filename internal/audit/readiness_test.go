package audit

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/metrics"
)

func TestDurableAdmissionRequiresWALAndRetainsEvidenceWithoutRequiredSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.wal")
	writer, err := NewWriter("test", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendDurable(context.Background(), Record{SchemaVersion: 1, Event: "key_create_requested"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path + ".intents")
	if err != nil || !bytes.Contains(content, []byte("key_create_requested")) {
		t.Fatalf("missing durable intent: %v", err)
	}
	for count := 0; count < queueCapacity; count++ {
		writer.Append(Record{SchemaVersion: 1, Event: "request_completed"})
	}
	if err := writer.AppendDurable(context.Background(), Record{SchemaVersion: 1, Event: "second_intent"}); err != nil {
		t.Fatalf("ordinary traffic accumulated behind intent: %v", err)
	}
	if err := writer.wal.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendDurable(context.Background(), Record{SchemaVersion: 1, Event: "must_refuse"}); err == nil {
		t.Fatal("failed WAL admitted mutation")
	}
	_ = writer.Close()
}

func TestIntentJournalRotatesWithoutLosingEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.wal")
	rec := Record{SchemaVersion: 1, Event: "key_create_requested", Instance: "old", PrevHash: genesisHash}
	line, err := rec.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat(append(line, '\n'), maxIntentRecords)
	if err := os.WriteFile(path+".intents", content, 0600); err != nil {
		t.Fatal(err)
	}
	for restart := 0; restart < 2; restart++ {
		writer, err := NewWriter("test", path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.AppendDurable(context.Background(), rec); err != nil {
			t.Fatalf("key issuance failed after journal filled: %v", err)
		}
		if ready, _ := writer.Ready(); !ready {
			t.Fatal("full intent journal blocked inference")
		}
		writer.Append(Record{SchemaVersion: 1, Event: "request_completed"})
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	segments, err := filepath.Glob(path + ".intents.segment-*")
	if err != nil || len(segments) != 1 {
		t.Fatalf("expected one archived segment, got %v (%v)", segments, err)
	}
	archived, err := os.ReadFile(segments[0])
	if err != nil || !bytes.Equal(content, archived) {
		t.Fatal("rotation changed retained intent evidence")
	}
	active, err := os.ReadFile(path + ".intents")
	if err != nil || bytes.Count(active, []byte("\n")) != 2 {
		t.Fatalf("new intents lost across restart: %v", err)
	}
}

func TestIntentArchiveLimitIsObservableAndRecoversAfterArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.wal")
	rec := Record{SchemaVersion: 1, Event: "key_create_requested", Instance: "old", PrevHash: genesisHash}
	line, err := rec.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	line = append(line, '\n')
	if err := os.WriteFile(path+".intents", bytes.Repeat(line, maxIntentRecords), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		if err := os.WriteFile(path+".intents.segment-"+strconv.Itoa(i), line, 0600); err != nil {
			t.Fatal(err)
		}
	}
	w, err := NewWriter("test", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	m := metrics.New()
	w.SetMetrics(m)
	if err := w.AppendDurable(context.Background(), rec); err == nil {
		t.Fatal("full archive admitted issuance")
	}
	if ready, _ := w.Ready(); !ready {
		t.Fatal("full intent archive blocked inference")
	}
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range families {
		if family.GetName() == "inferplane_audit_intent_utilization_ratio" {
			found = true
			if got := family.Metric[0].Gauge.GetValue(); got != 1 {
				t.Fatalf("full journal utilization = %v, want 1", got)
			}
		}
	}
	if !found {
		t.Fatal("intent capacity metric missing")
	}
	// An operator can first archive a completed segment elsewhere, then remove
	// its local copy without stopping the writer.
	if err := os.Remove(path + ".intents.segment-0"); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendDurable(context.Background(), rec); err != nil {
		t.Fatalf("issuance did not recover after archiving: %v", err)
	}
}

func TestMalformedIntentArchiveRefusesStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.wal")
	if err := os.WriteFile(path+".intents.segment-broken", []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter("test", path, nil)
	if err == nil {
		_ = w.Close()
		t.Fatal("malformed archived evidence admitted startup")
	}
}

func TestIntentRotationRejectsReplacedActiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.wal")
	rec := Record{SchemaVersion: 1, Event: "key_create_requested", Instance: "old", PrevHash: genesisHash}
	line, err := rec.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat(append(line, '\n'), maxIntentRecords)
	active := path + ".intents"
	if err := os.WriteFile(active, content, 0600); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter("test", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := os.Rename(active, active+".displaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(active, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendDurable(context.Background(), rec); err == nil {
		t.Fatal("issued key after the active intent journal was replaced")
	}
	if ready, _ := w.Ready(); ready {
		t.Fatal("replaced journal did not latch admission closed")
	}
	got, err := os.ReadFile(active + ".displaced")
	if err != nil || !bytes.Equal(got, content) {
		t.Fatal("replaced journal evidence was not preserved")
	}
}

func TestInterruptedIntentRotationPreservesEvidence(t *testing.T) {
	for _, activeRemains := range []bool{true, false} {
		t.Run(strconv.FormatBool(activeRemains), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.wal")
			rec := Record{SchemaVersion: 1, Event: "key_create_requested", Instance: "old", PrevHash: genesisHash}
			line, err := rec.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			content := bytes.Repeat(append(line, '\n'), maxIntentRecords)
			active := path + ".intents"
			archived := active + ".segment-before-crash"
			if err := os.WriteFile(active, content, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(active, archived); err != nil {
				t.Fatal(err)
			}
			if !activeRemains {
				if err := os.Remove(active); err != nil {
					t.Fatal(err)
				}
			}
			w, err := NewWriter("new", path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.AppendDurable(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(archived)
			if err != nil || !bytes.Equal(got, content) {
				t.Fatal("interrupted rotation lost archived evidence")
			}
		})
	}
}

func TestMalformedIntentJournalRefusesStartup(t *testing.T) {
	for _, content := range []string{`{"event":"secret"}`, "{\"event\":\"test\",\"instance\":\"test\",\"prev_hash\":\"invalid\"}\n"} {
		path := filepath.Join(t.TempDir(), "audit.wal")
		if err := os.WriteFile(path+".intents", []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		writer, err := NewWriter("test", path, nil)
		if err == nil {
			_ = writer.Close()
			t.Fatal("malformed journal admitted")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("journal contents leaked in error")
		}
	}
}

func TestIntentJournalFailureRetainsWALAndBlocksAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.wal")
	writer, err := NewWriter("test", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.intents.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendDurable(context.Background(), Record{SchemaVersion: 1, Event: "key_create_requested"}); err == nil {
		t.Fatal("failed intent journal admitted key creation")
	}
	if ready, _ := writer.Ready(); ready {
		t.Fatal("journal write failure did not block admission")
	}
	if err := writer.Close(); err == nil {
		t.Fatal("expected closed journal error")
	}
	content, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(content, []byte("key_create_requested")) {
		t.Fatal("failed journal lost WAL evidence")
	}
}

// Missing readiness represents the historical writer's always-admit posture,
// so these regressions exercise observable behavior on both implementations.
func writerReady(w *Writer) (bool, string) {
	if v, ok := any(w).(interface{ Ready() (bool, string) }); ok {
		return v.Ready()
	}
	return true, ""
}

func TestWriterRequiredSinkPressureBlocksAtQueueCapacity(t *testing.T) {
	for _, count := range []int{1023, 1024} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			w, err := NewWriter("test", filepath.Join(t.TempDir(), "audit.wal"), []Sink{&flakySink{failFirst: count}})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < count; i++ {
				w.Append(Record{SchemaVersion: 1, Event: "request_completed"})
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			ready, reason := writerReady(w)
			if ready != (count < 1024) || (!ready && reason == "") {
				t.Fatalf("%d retained records: ready=%v reason=%q", count, ready, reason)
			}
		})
	}
}

func TestWriterPressureStaysLatchedAfterRequiredSinkRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.wal")
	w, err := NewWriter("test", path, []Sink{&flakySink{failFirst: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1025; i++ {
		w.Append(Record{SchemaVersion: 1, Event: "request_completed"})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if ready, _ := writerReady(w); ready {
		t.Fatal("later successful sink writes cleared retained backlog pressure")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte{'\n'}) != 1025 {
		t.Fatal("in-flight settlement records were discarded after admission blocked")
	}
}

func TestWriterBestEffortFailuresDoNotBlockAdmission(t *testing.T) {
	w, err := NewWriter("test", filepath.Join(t.TempDir(), "audit.wal"), []Sink{NewWriterSink("stdout", errorWriter{}, false)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1025; i++ {
		w.Append(Record{SchemaVersion: 1, Event: "request_completed"})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if ready, reason := writerReady(w); !ready || reason != "" {
		t.Fatalf("best-effort sink failure blocks admission: %v %q", ready, reason)
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errTestFail }

func TestWriterWALFailureBlocksImmediatelyWithoutLeakingError(t *testing.T) {
	w, err := NewWriter("test", filepath.Join(t.TempDir(), "sensitive-path.wal"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.wal.f.Close(); err != nil {
		t.Fatal(err)
	}
	w.Append(Record{SchemaVersion: 1, Event: "request_completed"})
	_ = w.Close() // injected closed-file failure is expected
	ready, reason := writerReady(w)
	if ready || reason == "" || bytes.Contains([]byte(reason), []byte("sensitive-path")) {
		t.Fatalf("WAL failure did not close admission with a sanitized reason: %v %q", ready, reason)
	}
}

type truncateFailureSink struct{ closeWAL func() }

func (s truncateFailureSink) Write([]byte) error { s.closeWAL(); return nil }
func (truncateFailureSink) Required() bool       { return true }
func (truncateFailureSink) Name() string         { return "file" }
func (truncateFailureSink) Close() error         { return nil }

func TestWriterWALTruncateFailurePreservesPressureAndBlocks(t *testing.T) {
	var w *Writer
	sink := truncateFailureSink{closeWAL: func() { _ = w.wal.f.Close() }}
	var err error
	w, err = NewWriter("test", filepath.Join(t.TempDir(), "audit.wal"), []Sink{sink})
	if err != nil {
		t.Fatal(err)
	}
	before := WriteFailuresTotal()
	w.Append(Record{SchemaVersion: 1, Event: "request_completed"})
	_ = w.Close()
	if ready, _ := writerReady(w); ready || w.pending != 1 || WriteFailuresTotal() <= before {
		t.Fatalf("truncate failure was treated as delivery: ready=%v pending=%d failures=%d", ready, w.pending, WriteFailuresTotal()-before)
	}
}

func TestWriterRestartPreservesExistingBacklog(t *testing.T) {
	for _, count := range []int{1, 1024} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.wal")
			w, err := NewWriter("old", path, []Sink{&flakySink{failFirst: count}})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < count; i++ {
				w.Append(Record{SchemaVersion: 1, Event: "request_completed"})
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			w, err = NewWriter("new", path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if ready, _ := writerReady(w); ready != (count < 1024) {
				_ = w.Close()
				t.Fatalf("restart forgot retained pressure: count=%d ready=%v", count, ready)
			}
			w.Append(Record{SchemaVersion: 1, Event: "request_completed"})
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(after, before) || bytes.Count(after, []byte{'\n'}) != count+1 {
				t.Fatal("first successful write after restart truncated existing evidence")
			}
		})
	}
}

func TestWriterRefusesMalformedOrTruncatedRetainedWAL(t *testing.T) {
	for _, raw := range []string{
		`{"event":"partial"}`, "not-json\n", "null\n", "{}\n",
		"{\"event\":\"ok\",\"instance\":\"old\",\"prev_hash\":\"unverifiable\"}\n",
		"{\"event\":\"ok\",\"instance\":\"old\",\"prev_hash\":\"sha256:genesis\"}\n{\"event\":\"ok\",\"instance\":\"old\",\"prev_hash\":\"sha256:genesis\"}\n",
	} {
		path := filepath.Join(t.TempDir(), "audit.wal")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		w, err := NewWriter("new", path, nil)
		if err == nil {
			_ = w.Close()
			t.Fatalf("malformed retained WAL silently ignored: %q", raw)
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != raw {
			t.Fatal("invalid retained evidence was modified during failed startup")
		}
	}
}

func TestWriterRestartPreservesLargeValidRetainedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.wal")
	w, err := NewWriter("old", path, []Sink{&flakySink{failFirst: 1}})
	if err != nil {
		t.Fatal(err)
	}
	// Model names originate in bounded request bodies, but canonical audit
	// records have no smaller 1-MiB limit. Startup must accept writer output.
	w.Append(Record{
		SchemaVersion: 1, Event: "request_started",
		Request: RequestRef{ModelRequested: strings.Repeat("x", 2*1024*1024)},
	})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	next, err := NewWriter("new", path, nil)
	if err != nil {
		t.Fatalf("valid large retained record refused on restart: %v", err)
	}
	if ready, reason := next.Ready(); !ready {
		_ = next.Close()
		t.Fatalf("one valid retained record blocks admission: %s", reason)
	}
	next.Append(Record{SchemaVersion: 1, Event: "request_completed"})
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(after, before) || bytes.Count(after, []byte{'\n'}) != 2 {
		t.Fatal("large retained record was skipped or modified after restart")
	}
}
