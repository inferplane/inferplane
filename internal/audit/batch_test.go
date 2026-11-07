package audit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type batchProbe struct {
	started chan struct{}
	release chan struct{}
	sizes   []int
	path    string
}

func (s *batchProbe) Name() string         { return "batch" }
func (s *batchProbe) Required() bool       { return true }
func (s *batchProbe) Close() error         { return nil }
func (s *batchProbe) Write(b []byte) error { return s.WriteBatch([][]byte{b}) }
func (s *batchProbe) WriteBatch(lines [][]byte) error {
	s.sizes = append(s.sizes, len(lines))
	if len(s.sizes) == 1 {
		close(s.started)
		<-s.release
	}
	raw, err := os.ReadFile(s.path)
	if err != nil || len(raw) == 0 {
		return errTestFail
	}
	return nil
}

func TestWriterGroupsQueuedRecordsBeforeDurableAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	s := &batchProbe{started: make(chan struct{}), release: make(chan struct{}), path: path}
	w, err := NewWriter("batch", path, []Sink{s})
	if err != nil {
		t.Fatal(err)
	}
	w.Append(Record{Event: "first"})
	<-s.started
	for range 128 {
		w.Append(Record{Event: "queued"})
	}
	close(s.release)
	if err := w.AppendDurable(context.Background(), Record{Event: "durable"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, n := range s.sizes {
		total += n
	}
	if total != 130 || len(s.sizes) >= 10 {
		t.Fatalf("records were not grouped: %v", s.sizes)
	}
	if raw, err := os.ReadFile(path); err != nil || len(raw) != 0 {
		t.Fatalf("acknowledged spool not reclaimed: %d %v", len(raw), err)
	}
}
