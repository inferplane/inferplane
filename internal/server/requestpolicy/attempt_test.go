package requestpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/providers"
)

func TestRetryStatus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cross bool
		http  int
		want  string
	}{
		{"same-model 503", false, 503, audit.AttemptUpstreamError},
		{"cross 503", true, 503, audit.AttemptUpstreamError},
		{"cross 429", true, 429, audit.AttemptUpstreamError},
		{"cross 404", true, 404, audit.AttemptModelNotFound},
		{"cross 400", true, 400, audit.AttemptModelNotFound},
		{"cross transport error", true, 0, audit.AttemptUpstreamError},
		{"same-model 404", false, 404, audit.AttemptUpstreamError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := audit.WithEvidence(context.Background(), time.Now(), nil)
			audit.BeginAttempt(ctx, "p", "m")
			audit.NoteAttemptHTTP(ctx, tc.http)
			if got := RetryStatus(ctx, tc.cross); got != tc.want {
				t.Fatalf("RetryStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNoteAttemptFailure(t *testing.T) {
	ctx := audit.WithEvidence(context.Background(), time.Now(), nil)
	audit.BeginAttempt(ctx, "p", "m")
	NoteAttemptFailure(ctx, errors.New("dial tcp: refused"))
	if got := audit.OpenAttemptHTTP(ctx); got != 0 {
		t.Fatalf("plain error noted HTTP %d", got)
	}
	NoteAttemptFailure(ctx, &providers.UpstreamError{StatusCode: 503})
	if got := audit.OpenAttemptHTTP(ctx); got != 503 {
		t.Fatalf("upstream error HTTP = %d, want 503", got)
	}
}

func TestCountRecordStampsCompletedOnly(t *testing.T) {
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
	ctx := principal.With(req.Context(), keystore.Principal{KeyID: "ik_c", Team: "t"})
	ctx = audit.WithRouting(ctx, &audit.RoutingRef{RequestedModel: "m"})
	ctx = audit.WithEvidence(ctx, time.Now(), &audit.ClientRef{RequestClass: "main"})
	req = req.WithContext(ctx)
	CountRecord(w, req, "anthropic", false)
	CountRecord(w, req, "anthropic", true)
	w.Close()
	var recs []audit.Record
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var r audit.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d", len(recs))
	}
	if recs[0].Event != "request_started" || recs[0].Latency != nil || recs[0].Client != nil {
		t.Fatalf("started record changed: %+v", recs[0])
	}
	if recs[1].Event != "request_completed" || recs[1].Latency == nil || recs[1].Client == nil || recs[1].Client.RequestClass != "main" {
		t.Fatalf("completed record not stamped: %+v", recs[1])
	}
}

func TestAttemptStatusForHTTP(t *testing.T) {
	for code, want := range map[int]string{
		200: audit.AttemptOK, 399: audit.AttemptOK,
		400: audit.AttemptUpstreamError, 404: audit.AttemptUpstreamError, 429: audit.AttemptUpstreamError,
		500: audit.AttemptUpstreamError, 503: audit.AttemptUpstreamError, 504: audit.AttemptTimeout,
	} {
		if got := AttemptStatusForHTTP(code); got != want {
			t.Errorf("AttemptStatusForHTTP(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestEndAttemptOnError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		status   string
		wantHTTP int
	}{
		{"transport", errors.New("dial tcp: refused"), audit.AttemptUpstreamError, 0},
		{"deadline", context.DeadlineExceeded, audit.AttemptTimeout, 0},
		{"wrapped deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), audit.AttemptTimeout, 0},
		{"upstream 504", &providers.UpstreamError{StatusCode: 504}, audit.AttemptTimeout, 504},
		{"upstream 400", &providers.UpstreamError{StatusCode: 400}, audit.AttemptUpstreamError, 400},
		{"upstream 503", &providers.UpstreamError{StatusCode: 503}, audit.AttemptUpstreamError, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := audit.WithEvidence(context.Background(), time.Now(), nil)
			audit.BeginAttempt(ctx, "p", "m")
			EndAttemptOnError(ctx, tc.err)
			rec := audit.Record{Event: "request_completed"}
			audit.Finish(ctx, &rec)
			if len(rec.Attempts) != 1 || rec.Attempts[0].Status != tc.status || rec.Attempts[0].HTTP != tc.wantHTTP {
				t.Fatalf("attempts = %+v, want status %q http %d", rec.Attempts, tc.status, tc.wantHTTP)
			}
		})
	}
}
