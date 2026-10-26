package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func writeChain(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	w, _ := NewWriter("inst-1", filepath.Join(t.TempDir(), "a.wal"), []Sink{NewWriterSink("buf", &buf, true)})
	w.Append(Record{SchemaVersion: 1, Event: "request_started", ID: "01A"})
	w.Append(Record{SchemaVersion: 1, Event: "request_completed", ID: "01B"})
	w.Append(Record{SchemaVersion: 1, Event: "request_started", ID: "01C"})
	w.Close()
	return &buf
}

func TestVerifyAcceptsIntactChain(t *testing.T) {
	buf := writeChain(t)
	res, err := Verify(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Records != 3 {
		t.Fatalf("intact chain rejected: %+v", res)
	}
}

func TestVerifyAcceptsInstanceRestartSegments(t *testing.T) {
	// Two instances writing to the same file: each starts its own genesis-
	// anchored chain. A restart (new instance) must verify OK, not BROKEN.
	var buf bytes.Buffer
	w1, _ := NewWriter("inst-A", filepath.Join(t.TempDir(), "a.wal"), []Sink{NewWriterSink("b", &buf, true)})
	w1.Append(Record{SchemaVersion: 1, Event: "request_started", ID: "01A"})
	w1.Append(Record{SchemaVersion: 1, Event: "request_completed", ID: "01B"})
	w1.Close()
	w2, _ := NewWriter("inst-B", filepath.Join(t.TempDir(), "b.wal"), []Sink{NewWriterSink("b", &buf, true)})
	w2.Append(Record{SchemaVersion: 1, Event: "request_started", ID: "01C"})
	w2.Close()

	res, err := Verify(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Records != 3 {
		t.Fatalf("instance-segmented chain must verify OK across restart: %+v", res)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	buf := writeChain(t)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	// tamper: flip a byte in the first record's team-less body by replacing the ID.
	// The first record (01A) is line index 0; its bytes feed record 2's prev_hash,
	// so the break surfaces at the NEXT record (1-based BrokenAt == 2).
	lines[0] = strings.Replace(lines[0], `"01A"`, `"XXX"`, 1)
	tampered := strings.Join(lines, "\n") + "\n"
	res, _ := Verify(strings.NewReader(tampered))
	if res.OK {
		t.Fatal("tampering with a chained record must fail verification")
	}
	if res.BrokenAt != 2 {
		t.Fatalf("expected break detected at record 2 (the one after the tampered one), got %d", res.BrokenAt)
	}
}

func TestVerifyReportsPerInstanceHeadAndCount(t *testing.T) {
	buf := writeChain(t) // 3 records, one instance "inst-1"
	res, err := Verify(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	st, ok := res.Instances["inst-1"]
	if !ok {
		t.Fatal("Instances missing \"inst-1\"")
	}
	if st.Count != 3 {
		t.Errorf("Count = %d, want 3", st.Count)
	}
	if st.HeadHash == "" || st.HeadHash == genesisHash {
		t.Errorf("HeadHash = %q, want the real chain head", st.HeadHash)
	}
}

func TestVerifyTracksInstancesSeparatelyAcrossRestart(t *testing.T) {
	var buf bytes.Buffer
	w1, _ := NewWriter("inst-A", filepath.Join(t.TempDir(), "a.wal"), []Sink{NewWriterSink("b", &buf, true)})
	w1.Append(Record{SchemaVersion: 1, Event: "request_started", ID: "01A"})
	w1.Append(Record{SchemaVersion: 1, Event: "request_completed", ID: "01B"})
	w1.Close()
	w2, _ := NewWriter("inst-B", filepath.Join(t.TempDir(), "b.wal"), []Sink{NewWriterSink("b", &buf, true)})
	w2.Append(Record{SchemaVersion: 1, Event: "request_started", ID: "01C"})
	w2.Close()

	res, err := Verify(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Instances["inst-A"].Count != 2 {
		t.Errorf("inst-A count = %d, want 2", res.Instances["inst-A"].Count)
	}
	if res.Instances["inst-B"].Count != 1 {
		t.Errorf("inst-B count = %d, want 1", res.Instances["inst-B"].Count)
	}
}

func TestHeadAtCountMatchesVerifysFinalHead(t *testing.T) {
	buf := writeChain(t)
	full, err := Verify(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	want := full.Instances["inst-1"]
	got, found, err := HeadAtCount(strings.NewReader(buf.String()), "inst-1", want.Count)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("want found=true when the count exists in the stream")
	}
	if got != want.HeadHash {
		t.Errorf("HeadAtCount = %q, want %q (Verify's own final head)", got, want.HeadHash)
	}
}

func TestHeadAtCountFindsAMidChainCheckpoint(t *testing.T) {
	buf := writeChain(t) // 3 records
	// A checkpoint at count=2 must equal the head hash immediately after the
	// SECOND record — recomputed independently of the file's current tail.
	got, found, err := HeadAtCount(strings.NewReader(buf.String()), "inst-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("want found=true")
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	sum := sha256.Sum256([]byte(lines[1]))
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got != want {
		t.Errorf("HeadAtCount(2) = %q, want %q", got, want)
	}
}

func TestHeadAtCountNotFoundWhenChainIsShorterThanAnchor(t *testing.T) {
	buf := writeChain(t) // 3 records
	// An anchor witnessing count=10 for a chain that only has 3 records must
	// report "not found" — the truncation signal an anchor exists to catch.
	_, found, err := HeadAtCount(strings.NewReader(buf.String()), "inst-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("a chain shorter than the anchored count must report found=false")
	}
}

func TestHeadAtCountNotFoundOnBrokenChainBeforeReachingCount(t *testing.T) {
	buf := writeChain(t)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	lines[0] = strings.Replace(lines[0], `"01A"`, `"XXX"`, 1)
	tampered := strings.Join(lines, "\n") + "\n"
	_, found, err := HeadAtCount(strings.NewReader(tampered), "inst-1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("a chain broken before the target count must report found=false, not a wrong hash")
	}
}

func largeVerifyChain(t *testing.T) ([]byte, []string) {
	t.Helper()
	var stream bytes.Buffer
	var heads []string
	prev := genesisHash
	for i, rec := range []Record{
		{SchemaVersion: 1, Instance: "large", Event: "request_started", Request: RequestRef{ModelRequested: strings.Repeat("x", 2*1024*1024+17)}},
		{SchemaVersion: 1, Instance: "large", Event: "request_completed"},
		{SchemaVersion: 1, Instance: "restart", Event: "request_started"},
	} {
		if rec.Instance == "restart" {
			prev = genesisHash
		}
		rec.PrevHash = prev
		line, err := rec.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		// Valid JSON formatting is part of the line bytes, not something the
		// verifier may reconstruct by decoding and reserializing the record.
		if i == 0 {
			line = append(line, ' ', '\t')
		} else if i == 1 {
			line = append([]byte(" \t"), line...)
		}
		sum := sha256.Sum256(line)
		prev = "sha256:" + hex.EncodeToString(sum[:])
		heads = append(heads, prev)
		stream.Write(line)
		stream.WriteByte('\n')
	}
	return stream.Bytes(), heads
}

func TestVerifyAcceptsLargeExactByteChain(t *testing.T) {
	stream, heads := largeVerifyChain(t)
	result, err := Verify(bytes.NewReader(stream))
	if err != nil || !result.OK || result.Records != 3 {
		t.Fatalf("large valid multi-segment chain rejected: result=%+v err=%v", result, err)
	}
	if result.Instances["large"] != (InstanceState{HeadHash: heads[1], Count: 2}) ||
		result.Instances["restart"] != (InstanceState{HeadHash: heads[2], Count: 1}) {
		t.Fatalf("exact-byte instance heads differ from independently hashed lines: %+v", result.Instances)
	}
}

func TestHeadAtCountAcceptsLargeExactByteCheckpoints(t *testing.T) {
	stream, heads := largeVerifyChain(t)
	for _, checkpoint := range []struct {
		instance string
		count    int64
		head     string
	}{
		{"large", 1, heads[0]}, {"large", 2, heads[1]}, {"restart", 1, heads[2]},
	} {
		head, found, err := HeadAtCount(bytes.NewReader(stream), checkpoint.instance, checkpoint.count)
		if err != nil || !found || head != checkpoint.head {
			t.Fatalf("large anchored checkpoint %s/%d: head=%q found=%v err=%v", checkpoint.instance, checkpoint.count, head, found, err)
		}
	}
}

func TestVerifyLargeRecordDetectsByteTampering(t *testing.T) {
	stream, heads := largeVerifyChain(t)
	for _, tc := range []struct {
		name, before, after string
	}{
		{"model", strings.Repeat("x", 32), strings.Repeat("x", 31) + "y"},
		{"json whitespace", "} \t\n", "}  \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tampered := bytes.Replace(stream, []byte(tc.before), []byte(tc.after), 1)
			if bytes.Equal(tampered, stream) {
				t.Fatal("tamper did not change fixture bytes")
			}
			result, err := Verify(bytes.NewReader(tampered))
			if err != nil || result.OK || result.BrokenAt != 2 {
				t.Fatalf("large-record byte change not detected at next record: result=%+v err=%v", result, err)
			}
			head, found, err := HeadAtCount(bytes.NewReader(tampered), "large", 1)
			if err != nil || !found || head == heads[0] {
				t.Fatalf("external first-record anchor did not detect changed bytes: head=%q found=%v err=%v", head, found, err)
			}
			if _, found, err := HeadAtCount(bytes.NewReader(tampered), "large", 2); err != nil || found {
				t.Fatalf("broken large chain reached later anchor: found=%v err=%v", found, err)
			}
		})
	}
}

func TestVerifyRetainsLineFramingAndTrailingEOFBehavior(t *testing.T) {
	stream := writeChain(t).Bytes()
	want, err := Verify(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	crlf := bytes.ReplaceAll(stream, []byte{'\n'}, []byte{'\r', '\n'})
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"LF", stream},
		{"CRLF", crlf},
		{"final JSON without newline", bytes.TrimSuffix(stream, []byte{'\n'})},
		{"final JSON followed by CR", bytes.TrimSuffix(crlf, []byte{'\n'})},
		{"empty lines", append([]byte("\r\n\n"), stream...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Verify(bytes.NewReader(tc.data))
			if err != nil || !result.OK || result.Records != want.Records || result.Instances["inst-1"] != want.Instances["inst-1"] {
				t.Fatalf("established framing changed: result=%+v err=%v", result, err)
			}
			head, found, err := HeadAtCount(bytes.NewReader(tc.data), "inst-1", 3)
			if err != nil || !found || head != want.Instances["inst-1"].HeadHash {
				t.Fatalf("checkpoint framing changed: head=%q found=%v err=%v", head, found, err)
			}
		})
	}
}

type verifyFailureReader struct {
	data []byte
	err  error
}

func (r *verifyFailureReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err // includes the permitted simultaneous data+error case
	}
	return n, nil
}

func TestVerifyRetainsReaderErrorAndEarlyAnchorBehavior(t *testing.T) {
	stream := writeChain(t).Bytes()
	sentinel := errors.New("injected audit read error")
	// Both a terminated record and the old ScanLines final-EOF token behavior
	// must preserve real reader errors after any buffered records are inspected.
	for _, data := range [][]byte{stream, bytes.TrimSuffix(stream, []byte{'\n'})} {
		reader := func() io.Reader { return &verifyFailureReader{data: data, err: sentinel} }
		if _, err := Verify(reader()); !errors.Is(err, sentinel) {
			t.Fatalf("reader failure converted into successful verification: %v", err)
		}
		if _, found, err := HeadAtCount(reader(), "inst-1", 3); err != nil || !found {
			t.Fatalf("already reached checkpoint lost to later reader error: found=%v err=%v", found, err)
		}
		if _, found, err := HeadAtCount(reader(), "inst-1", 4); found || !errors.Is(err, sentinel) {
			t.Fatalf("unreached checkpoint swallowed reader error: found=%v err=%v", found, err)
		}
	}
	// A malformed final token is reported as broken evidence before a later
	// reader error, as it was under ScanLines; it is not silently skipped.
	malformed := &verifyFailureReader{data: []byte("{"), err: sentinel}
	if result, err := Verify(malformed); err != nil || result.OK || result.BrokenAt != 1 {
		t.Fatalf("malformed buffered token changed classification: result=%+v err=%v", result, err)
	}
}
