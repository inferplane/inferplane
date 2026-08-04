// Usage telemetry ingestion + query (the control plane's "telemetry up"
// half; policy distribution in controlplane.go is "config down"). Mounted
// independently of the policy Server: a telemetry-only inferplaned (no
// --policies) is a valid deployment.
package controlplane

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
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
