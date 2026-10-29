package analytics

import (
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
)

func TestSink_ingestsCompletedIgnoresRest(t *testing.T) {
	ix, _ := OpenSQLite(":memory:")
	defer ix.Close()
	s := NewSink(ix)
	if s.Required() {
		t.Fatal("analytics sink must be best-effort (Required()==false)")
	}
	if s.Name() != "analytics" {
		t.Fatalf("name = %q, want analytics", s.Name())
	}
	if err := s.Write([]byte(`{"event":"request_completed","id":"x1","ts":"2026-06-29T00:00:00Z","principal":{"team":"t"},"request":{"model_resolved":"m"},"cost":{"amount_usd_micros":7}}`)); err != nil {
		t.Fatalf("write completed: %v", err)
	}
	if err := s.Write([]byte(`{"event":"request_started","id":"x2","ts":"2026-06-29T00:00:00Z"}`)); err != nil {
		t.Fatalf("write started: %v", err)
	}
	if err := s.Write([]byte(`not json`)); err != nil {
		t.Fatalf("malformed line must not error the chain: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, _ := ix.Summary(SummaryQuery{})
	if s2.Totals.Requests != 1 || s2.Totals.CostMicros != 7 {
		t.Fatalf("got %d/%d, want 1/7", s2.Totals.Requests, s2.Totals.CostMicros)
	}
}

func TestSinkIncludesCostlessCompletion(t *testing.T) {
	ix, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	s := NewSink(ix)
	for i := 0; i < 64; i++ {
		line := []byte(`{"event":"request_completed","id":"costless-` + time.Unix(int64(i), 0).UTC().Format(time.RFC3339) + `","outcome":{"status":500}}`)
		if err := s.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Write([]byte(`{"event":"admin_denied","id":"admin","cost":{"amount_usd_micros":42}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	summary, err := ix.Summary(SummaryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := OperationTotals{Completed: 64, Failed: 64, UnavailableRequests: 64}
	if summary.Totals.Operations != want || summary.Totals.Requests != 0 || summary.Totals.CostMicros != 0 {
		t.Fatalf("close did not drain costless completions: %+v", summary.Totals)
	}
}

func TestSinkFullQueueDoesNotBlock(t *testing.T) {
	// A real full queue with no worker makes blocking enqueue observable.
	s := &sink{ch: make(chan sinkItem, sinkQueueCap)}
	for i := 0; i < cap(s.ch); i++ {
		s.ch <- sinkItem{rec: audit.Record{Event: "request_completed"}}
	}
	done := make(chan error, 1)
	go func() {
		done <- s.Write([]byte(`{"event":"request_completed","id":"dropped"}`))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		<-s.ch // release a bad blocking implementation before failing
		<-done
		t.Fatal("full queue blocked Write")
	}
	if len(s.ch) != cap(s.ch) {
		t.Fatal("full-queue drop changed buffered records")
	}
}
