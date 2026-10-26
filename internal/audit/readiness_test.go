package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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
