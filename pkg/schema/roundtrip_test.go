package schema

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoldenRoundTrip — the enforcement point for the lossless round-trip
// invariant. Each file under testdata/roundtrip/{request,response}/*.json is
// unmarshaled then marshaled and compared for semantic equality. Adding a
// fixture automatically puts it under test (captured real Claude Code traffic
// belongs here).
func TestGoldenRoundTrip(t *testing.T) {
	kinds := map[string]func([]byte) ([]byte, error){
		"request": func(in []byte) ([]byte, error) {
			var v ChatRequest
			if err := v.UnmarshalJSON(in); err != nil {
				return nil, err
			}
			return v.MarshalJSON()
		},
		"response": func(in []byte) ([]byte, error) {
			var v ChatResponse
			if err := v.UnmarshalJSON(in); err != nil {
				return nil, err
			}
			return v.MarshalJSON()
		},
	}
	for kind, roundTrip := range kinds {
		files, err := filepath.Glob(filepath.Join("testdata", "roundtrip", kind, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no golden fixtures found (err=%v)", kind, err)
		}
		for _, f := range files {
			t.Run(kind+"/"+filepath.Base(f), func(t *testing.T) {
				in, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				out, err := roundTrip(in)
				if err != nil {
					t.Fatal(err)
				}
				assertJSONSemanticEqual(t, in, out)
			})
		}
	}
}

// TestGoldenStreamRoundTrip — round-trips each `data:` line of a .sse
// fixture through ChatChunk. Event order is guaranteed by file order.
func TestGoldenStreamRoundTrip(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "roundtrip", "stream", "*.sse"))
	if err != nil || len(files) == 0 {
		t.Fatalf("stream: no golden fixtures found (err=%v)", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			sc := bufio.NewScanner(bytes.NewReader(raw))
			sc.Buffer(make([]byte, 1024*1024), 1024*1024)
			for sc.Scan() {
				line := sc.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				payload := []byte(strings.TrimPrefix(line, "data: "))
				var c ChatChunk
				if err := c.UnmarshalJSON(payload); err != nil {
					t.Fatalf("event %d: %v", n, err)
				}
				out, err := c.MarshalJSON()
				if err != nil {
					t.Fatalf("event %d: %v", n, err)
				}
				assertJSONSemanticEqual(t, payload, out)
				n++
			}
			if n == 0 {
				t.Fatal("no data: events in fixture")
			}
		})
	}
}
