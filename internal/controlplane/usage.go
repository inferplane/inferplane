// Usage telemetry ingestion + query (the control plane's "telemetry up"
// half; policy distribution in controlplane.go is "config down"). Mounted
// independently of the policy Server: a telemetry-only inferplaned (no
// --policies) is a valid deployment.
package controlplane

import (
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/inferplane/inferplane/internal/telemetry"
)

// usageMaxBodyBytes bounds a usage batch: bigger than the 1 MiB sync bound
// because a batch carries per-(team,user,model) rows and a large fleet's
// cardinality can legitimately outgrow 1 MiB.
const usageMaxBodyBytes = 4 << 20

// UsageServer serves POST /v1alpha1/usage (data-plane batch ingestion) and
// GET /v1alpha1/usage (operator queries) over an Aggregator. It shares the
// control plane's bearer-token posture but is deliberately NOT part of the
// policy Server — telemetry must work without a policy source.
type UsageServer struct {
	token string
	agg   telemetry.Aggregator
}

// NewUsageServer builds the usage endpoints over agg with the shared bearer
// token (empty = unauthenticated, loopback-only — main.go enforces that).
func NewUsageServer(token string, agg telemetry.Aggregator) *UsageServer {
	return &UsageServer{token: token, agg: agg}
}

// Mount registers the usage endpoints on mux.
func (s *UsageServer) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1alpha1/usage", s.auth(s.handleIngest))
	mux.HandleFunc("GET /v1alpha1/usage", s.auth(s.handleQuery))
	mux.HandleFunc("GET /v1alpha1/usage/export", s.auth(s.handleExport))
}

// auth mirrors Server.auth (constant-time bearer comparison) — duplicated
// rather than shared so neither server type depends on the other existing.
func (s *UsageServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + s.token
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (s *UsageServer) handleIngest(w http.ResponseWriter, r *http.Request) {
	var b telemetry.UsageBatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, usageMaxBodyBytes)).Decode(&b); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, `{"error":"batch too large"}`, http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, `{"error":"bad usage batch"}`, http.StatusBadRequest)
		return
	}
	if err := b.Validate(); err != nil {
		// Invalid content can never be stored — a 4xx tells the data plane
		// to drop it (its pusher treats 4xx as permanent), keeping a poison
		// batch from wedging its retry FIFO.
		http.Error(w, `{"error":"invalid usage batch"}`, http.StatusBadRequest)
		return
	}
	if err := s.agg.Upsert(r.Context(), &b); err != nil {
		// Durable-store failure (Task 8): the ack means durable, so a failed
		// write is a 503 — the data plane's FIFO keeps the batch and
		// retries. Never ack what wasn't stored.
		http.Error(w, `{"error":"usage store unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *UsageServer) handleQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := telemetry.QueryFilter{
		Team:    q.Get("team"),
		User:    q.Get("user"),
		Model:   q.Get("model"),
		GroupBy: q.Get("group_by"),
	}
	if f.GroupBy == "" {
		f.GroupBy = "team"
	}
	var err error
	if f.Since, err = parseTimeParam(q.Get("since"), time.Time{}); err != nil {
		http.Error(w, `{"error":"bad since"}`, http.StatusBadRequest)
		return
	}
	// Until defaults to +∞-ish: "everything since".
	if f.Until, err = parseTimeParam(q.Get("until"), time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		http.Error(w, `{"error":"bad until"}`, http.StatusBadRequest)
		return
	}
	// group_by is the only client-caused Query error — validate it HERE so a
	// backend failure (durable store down, Task 8) is never misreported as a
	// client error.
	if !telemetry.ValidGroupBy(f.GroupBy) {
		http.Error(w, `{"error":"bad group_by"}`, http.StatusBadRequest)
		return
	}
	res, err := s.agg.Query(r.Context(), f)
	if err != nil {
		http.Error(w, `{"error":"usage store unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// parseTimeParam accepts RFC3339 or a bare date (2026-08-04); empty → def.
func parseTimeParam(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

// handleExport streams finest-granularity rows as CSV or JSON. since/until
// are REQUIRED so an export is always an explicitly bounded range (plan r3).
// Rows are written as the callback fires — nothing materializes server-side —
// and the per-write deadline is extended via ResponseController because the
// server's global WriteTimeout (30s, an ADR-034 slow-drip guard) would
// otherwise silently truncate a long export (CSV has no trailer). The rolling
// deadline still cuts off a STALLED client; it just doesn't cap total
// transfer time. A durable-store failure aborts with 503 up front (and
// mid-stream simply terminates the connection — never a silent partial file
// presented as complete, and never a mid-cursor fallback).
func (s *UsageServer) handleExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("since") == "" || q.Get("until") == "" {
		http.Error(w, `{"error":"since and until are required"}`, http.StatusBadRequest)
		return
	}
	since, err := parseTimeParam(q.Get("since"), time.Time{})
	if err != nil {
		http.Error(w, `{"error":"bad since"}`, http.StatusBadRequest)
		return
	}
	until, err := parseTimeParam(q.Get("until"), time.Time{})
	if err != nil {
		http.Error(w, `{"error":"bad until"}`, http.StatusBadRequest)
		return
	}
	format := q.Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		http.Error(w, `{"error":"format must be csv or json"}`, http.StatusBadRequest)
		return
	}

	rc := http.NewResponseController(w)
	extend := func() { _ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second)) }

	var writeRow func(telemetry.StoredRow) error
	var finish func()
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		cw := csv.NewWriter(w)
		header := []string{"dataplane", "window_start", "window_end", "team", "user", "model",
			"spent_micro_usd", "input_tokens", "output_tokens",
			"cache_read_tokens", "cache_write_5m_tokens", "cache_write_1h_tokens"}
		headerWritten := false
		writeRow = func(row telemetry.StoredRow) error {
			if !headerWritten {
				if err := cw.Write(header); err != nil {
					return err
				}
				headerWritten = true
			}
			extend()
			return cw.Write([]string{
				row.Dataplane, row.WindowStart.Format(time.RFC3339Nano), row.WindowEnd.Format(time.RFC3339Nano),
				row.Team, row.User, row.Model,
				strconv.FormatInt(row.SpentMicroUSD, 10), strconv.FormatInt(row.InputTokens, 10),
				strconv.FormatInt(row.OutputTokens, 10), strconv.FormatInt(row.CacheReadTokens, 10),
				strconv.FormatInt(row.CacheWrite5mTokens, 10), strconv.FormatInt(row.CacheWrite1hTokens, 10),
			})
		}
		finish = func() {
			if !headerWritten {
				_ = cw.Write(header) // empty export still yields the header
			}
			cw.Flush()
		}
	default: // json — a stream of row objects inside an array
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		first := true
		writeRow = func(row telemetry.StoredRow) error {
			// The opening bracket is written lazily with the FIRST row so a
			// store failure before any output can still send a clean 503
			// (writing it eagerly would commit the 200).
			sep := ","
			if first {
				sep = "["
				first = false
			}
			if _, err := w.Write([]byte(sep)); err != nil {
				return err
			}
			extend()
			return enc.Encode(row)
		}
		finish = func() {
			if first {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_, _ = w.Write([]byte("]"))
		}
	}

	// Probe availability BEFORE committing the 200: run Rows and let the
	// first callback commit the response. If Rows fails before any row was
	// written, we can still send a clean 503.
	wroteAny := false
	err = s.agg.Rows(r.Context(), since, until, func(row telemetry.StoredRow) error {
		wroteAny = true
		return writeRow(row)
	})
	if err != nil && !wroteAny {
		http.Error(w, `{"error":"usage store unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	// err != nil after rows were written: the stream is already committed —
	// terminating without finish() leaves an unterminated body the client's
	// parser rejects, which is the honest signal.
	if err == nil {
		finish()
	}
}
