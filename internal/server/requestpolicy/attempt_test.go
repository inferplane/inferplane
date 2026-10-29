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

func closedAttempt(t *testing.T, ctx context.Context) audit.AttemptRef {
	t.Helper()
	rec := audit.Record{Event: "request_completed"}
	audit.Finish(ctx, &rec)
	if len(rec.Attempts) != 1 {
		t.Fatalf("attempts = %+v", rec.Attempts)
	}
	return rec.Attempts[0]
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return audit.WithEvidence(ctx, time.Now(), nil)
}

func TestEndRetriedAttempt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		httpStatus int
		mnf        bool
		status     string
		wantHTTP   int
	}{
		{"transport", errors.New("dial"), 0, false, audit.AttemptUpstreamError, 0},
		{"upstream 503 err", &providers.UpstreamError{StatusCode: 503}, 0, false, audit.AttemptUpstreamError, 503},
		{"upstream 401 err not model-not-found", &providers.UpstreamError{StatusCode: 401}, 0, false, audit.AttemptUpstreamError, 401},
		{"upstream 404 err model-not-found", &providers.UpstreamError{StatusCode: 404}, 0, true, audit.AttemptModelNotFound, 404},
		{"status 429", nil, 429, false, audit.AttemptUpstreamError, 429},
		{"status 404 model-not-found", nil, 404, true, audit.AttemptModelNotFound, 404},
		{"status 500", nil, 500, false, audit.AttemptUpstreamError, 500},
		{"status 504 timeout", nil, 504, false, audit.AttemptTimeout, 504},
		{"deadline", context.DeadlineExceeded, 0, false, audit.AttemptTimeout, 0},
		{"canceled err", context.Canceled, 0, true, audit.AttemptCanceled, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := audit.WithEvidence(context.Background(), time.Now(), nil)
			audit.BeginAttempt(ctx, "p", "m")
			EndRetriedAttempt(ctx, tc.err, tc.httpStatus, tc.mnf)
			if a := closedAttempt(t, ctx); a.Status != tc.status || a.HTTP != tc.wantHTTP {
				t.Fatalf("attempt = %+v, want %s/%d", a, tc.status, tc.wantHTTP)
			}
		})
	}
	t.Run("canceled context", func(t *testing.T) {
		ctx := canceledCtx()
		audit.BeginAttempt(ctx, "p", "m")
		EndRetriedAttempt(ctx, errors.New("x"), 0, false)
		if a := closedAttempt(t, ctx); a.Status != audit.AttemptCanceled {
			t.Fatalf("attempt = %+v", a)
		}
	})
}

func TestEndAttemptOnErrorCanceled(t *testing.T) {
	for name, ctx := range map[string]context.Context{"err": audit.WithEvidence(context.Background(), time.Now(), nil), "ctx": canceledCtx()} {
		t.Run(name, func(t *testing.T) {
			err := errors.New("x")
			if name == "err" {
				err = fmt.Errorf("w: %w", context.Canceled)
			}
			audit.BeginAttempt(ctx, "p", "m")
			EndAttemptOnError(ctx, err)
			if a := closedAttempt(t, ctx); a.Status != audit.AttemptCanceled {
				t.Fatalf("attempt = %+v", a)
			}
		})
	}
}

func TestEndStreamAttempt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		completed bool
		err       error
		ctx       func() context.Context
		status    string
	}{
		{"clean", true, nil, func() context.Context { return audit.WithEvidence(context.Background(), time.Now(), nil) }, audit.AttemptOK},
		{"incomplete", false, nil, func() context.Context { return audit.WithEvidence(context.Background(), time.Now(), nil) }, audit.AttemptStreamFailed},
		{"error", true, errors.New("reset"), func() context.Context { return audit.WithEvidence(context.Background(), time.Now(), nil) }, audit.AttemptStreamFailed},
		{"canceled err", false, context.Canceled, func() context.Context { return audit.WithEvidence(context.Background(), time.Now(), nil) }, audit.AttemptCanceled},
		{"canceled ctx", false, errors.New("x"), canceledCtx, audit.AttemptCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tc.ctx()
			audit.BeginAttempt(ctx, "p", "m")
			EndStreamAttempt(ctx, tc.completed, tc.err)
			if a := closedAttempt(t, ctx); a.Status != tc.status || a.HTTP != 200 {
				t.Fatalf("attempt = %+v, want %s", a, tc.status)
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
