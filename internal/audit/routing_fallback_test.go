package audit

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutingFromCarriesModelFallback(t *testing.T) {
	ctx := WithModelFallback(context.Background(), "claude-sonnet-9", "claude-sonnet-5-5", "family")
	got := RoutingFrom(ctx)
	if got == nil || got.Reason != "model_fallback" || got.RequestedModel != "claude-sonnet-9" || got.SelectedModel != "claude-sonnet-5-5" ||
		got.ModelFallbackFrom != "claude-sonnet-9" || got.ModelFallbackSource != "family" {
		t.Fatalf("legacy request lost its fallback evidence: %+v", got)
	}
	// A policy decision keeps its own reason and gains the evidence.
	ctx = WithRouting(ctx, &RoutingRef{RequestedModel: "claude-sonnet-5-5", Reason: "context_selected", Inspection: "complete"})
	got = RoutingFrom(ctx)
	if got.Reason != "context_selected" || got.RequestedModel != "claude-sonnet-5-5" ||
		got.ModelFallbackFrom != "claude-sonnet-9" || got.ModelFallbackSource != "family" {
		t.Fatalf("policy decision lost or replaced evidence: %+v", got)
	}
	// Attempt fields ride along with the evidence.
	if got := RoutingFrom(WithRoutingAttempt(ctx, "claude-sonnet-5-5", "public", "")); got.ActualProvider != "public" || got.ModelFallbackSource != "family" {
		t.Fatalf("attempt dropped the evidence: %+v", got)
	}
	if RoutingFrom(context.Background()) != nil {
		t.Fatal("a request without routing or fallback must keep omitting routing")
	}
	long := RoutingFrom(WithModelFallback(context.Background(), strings.Repeat("x", 1000), "m", "family"))
	if len(long.ModelFallbackFrom) != maxFallbackRequested || len(long.RequestedModel) != maxFallbackRequested {
		t.Fatalf("client model name not bounded: %d", len(long.ModelFallbackFrom))
	}
}

// Records with fallback evidence verify in a chain beside records written
// before the fields existed, and records without a fallback keep their bytes.
func TestModelFallbackRecordsVerifyInMixedChains(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []Sink{NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	legacy := Record{SchemaVersion: 1, Event: "request_completed", ID: "01A", TS: "2026-10-29T00:00:00Z",
		Request: RequestRef{Ingress: "anthropic", ModelRequested: "m"}, Outcome: &OutcomeRef{Status: 200}}
	before, err := legacy.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithModelFallback(context.Background(), "claude-sonnet-9", "m", "family")
	fallback := legacy
	fallback.ID = "01B"
	fallback.Request.Routing = RoutingFrom(ctx)
	w.Append(legacy)
	w.Append(fallback)
	w.Close()
	res, err := Verify(bytes.NewReader(buf.Bytes()))
	if err != nil || !res.OK || res.Records != 2 {
		t.Fatalf("verify = %+v, %v", res, err)
	}
	after, _ := legacy.Canonical()
	if !bytes.Equal(before, after) || bytes.Contains(after, []byte("model_fallback")) {
		t.Fatalf("legacy bytes changed: %s", after)
	}
	if !strings.Contains(buf.String(), `"reason":"model_fallback"`) || !strings.Contains(buf.String(), `"model_fallback_source":"family"`) {
		t.Fatalf("fallback evidence missing: %s", buf.String())
	}
}
