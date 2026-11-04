package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterSinkAppendsLines(t *testing.T) {
	var buf bytes.Buffer
	s := NewWriterSink("test", &buf, false)
	if err := s.Write([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "{\"a\":1}\n{\"a\":2}\n" {
		t.Fatalf("sink should append newline-delimited JSONL: %q", buf.String())
	}
	if s.Required() {
		t.Fatal("test sink declared required=false")
	}
}

func TestFileSinkPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	s, err := NewFileSink(path, true)
	if err != nil {
		t.Fatal(err)
	}
	s.Write([]byte(`{"x":1}`))
	s.Close()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `{"x":1}`) {
		t.Fatalf("file sink did not persist: %q", data)
	}
}

type recordingSink struct {
	recs  []Record
	canon [][]byte
}

func (s *recordingSink) Write([]byte) error { panic("RecordSink must receive WriteRecord, not Write") }
func (s *recordingSink) WriteRecord(rec *Record, canon []byte) error {
	s.recs = append(s.recs, *rec)
	s.canon = append(s.canon, append([]byte(nil), canon...))
	return nil
}
func (s *recordingSink) Name() string   { return "recording" }
func (s *recordingSink) Required() bool { return false }
func (s *recordingSink) Close() error   { return nil }

func TestWriterDispatchesRecordSinks(t *testing.T) {
	rs := &recordingSink{}
	var lines bytes.Buffer
	w, err := NewWriter("i1", filepath.Join(t.TempDir(), "wal"), []Sink{rs, NewWriterSink("lines", &lines, true)})
	if err != nil {
		t.Fatal(err)
	}
	w.Append(Record{SchemaVersion: 1, Event: "request_completed", ID: "r1", TS: "2026-10-27T00:00:00Z",
		Principal: PrincipalRef{KeyID: "ik_x", Team: "eng", Account: "alice"}})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rs.recs) != 1 || rs.recs[0].Principal.Account != "alice" || rs.recs[0].Instance != "i1" {
		t.Fatalf("record sink got %+v", rs.recs)
	}
	if got := strings.TrimSuffix(lines.String(), "\n"); got != string(rs.canon[0]) {
		t.Fatalf("record sink canonical bytes differ from the chain line:\n%s\n%s", rs.canon[0], got)
	}
}

func TestPrincipalAccountNeverSerialized(t *testing.T) {
	base := Record{SchemaVersion: 1, Event: "request_completed", ID: "r1", Principal: PrincipalRef{KeyID: "ik_x", Team: "eng"}}
	withAccount := base
	withAccount.Principal.Account = "alice@example.com"
	a, err := base.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	b, err := withAccount.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("Account changed the hash-chained bytes:\n%s\n%s", a, b)
	}
}
