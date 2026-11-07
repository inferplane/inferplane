package responses

import (
	"bytes"
	"testing"
)

func TestParsedRequestPreservesWireAndChecks(t *testing.T) {
	raw := []byte(`{ "model":"m", "input":"hello", "store":false, "metadata":{"n":9007199254740993}, "tools":[{"type":"function","name":"f","parameters":{"type":"object"}}] }`)
	original := bytes.Clone(raw)
	doc, err := ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if doc.Canonical().Model != "m" || doc.StoredReference() || doc.ValidateConversion() != nil {
			t.Fatal("valid stateless request refused")
		}
		strict, err := doc.StrictTools()
		if !strict || err != nil {
			t.Fatalf("implicit strict tools lost: %v %v", strict, err)
		}
	}
	if !bytes.Equal(raw, original) {
		t.Fatal("observation changed native bytes")
	}
}

func TestParsedRequestRefusals(t *testing.T) {
	for _, raw := range []string{
		`{"model":"m","input":"x","input":"y"}`,
		`{"model":"m","input":"x","opaque":{"x":1,"x":2}}`,
		`{"model":"m","input":"x"} {}`,
	} {
		if _, err := ParseRequest([]byte(raw)); err == nil {
			t.Fatalf("invalid JSON accepted: %s", raw)
		}
	}
	for _, raw := range []string{
		`{"model":"m","input":"x","previous_response_id":"other"}`,
		`{"model":"m","input":[{"type":"item_reference","id":"other"}]}`,
	} {
		doc, err := ParseRequest([]byte(raw))
		if err != nil || !doc.StoredReference() || doc.ValidateConversion() == nil {
			t.Fatalf("provider-owned state accepted: %v", err)
		}
	}
	doc, err := ParseRequest([]byte(`{"model":"m","input":"x","opaque":{"n":9007199254740993}}`))
	if err != nil || doc.ValidateConversion() == nil {
		t.Fatalf("opaque native field converted: %v", err)
	}
}

func BenchmarkParsedRequestChecks(b *testing.B) {
	raw := append([]byte(`{"model":"m","input":"`), bytes.Repeat([]byte("a"), 1<<20)...)
	raw = append(raw, []byte(`","store":false}`)...)
	b.ReportAllocs()
	for b.Loop() {
		doc, err := ParseRequest(raw)
		if err != nil {
			b.Fatal(err)
		}
		_ = doc.StoredReference()
		for range 3 {
			if err := doc.ValidateConversion(); err != nil {
				b.Fatal(err)
			}
			_, _ = doc.StrictTools()
		}
	}
}
