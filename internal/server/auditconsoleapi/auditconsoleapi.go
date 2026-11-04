// Package auditconsoleapi serves the read-only audit console API (ADR-057):
// /admin/audit/events, /events/{id}, /sessions, /sessions/{ref}, /summary.
// Mounted behind AdminAuth + requireAdmin. Every error is a closed code; no
// handler echoes query input or internal error text back to the client.
package auditconsoleapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/inferplane/inferplane/internal/analytics"
)

const (
	defaultLimit     = 100
	listLimit        = 200
	sessionLimit     = 500
	maxWindow        = 366 * 24 * time.Hour
	defaultWindow    = 24 * time.Hour
	maxFreeTextBytes = 256
)

var (
	ulidRE    = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	sessionRE = regexp.MustCompile(`^s1:[0-9a-f]{32}$`)
	traceRE   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	codeRE    = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

// storageFail logs the storage error (no request values, no session id) and
// returns the generic closed-code 500.
func storageFail(w http.ResponseWriter, op string, err error) {
	slog.Error("audit console query failed", "op", op, "err", err)
	fail(w, http.StatusInternalServerError, "query_failed")
}

// ParseFilter validates query parameters. The second result is a closed error code, "" when valid.
func ParseFilter(v url.Values, now time.Time, maxLimit int) (analytics.AuditFilter, string) {
	f := analytics.AuditFilter{Limit: defaultLimit}
	until, since := now, now.Add(-defaultWindow)
	if s := v.Get("until"); s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return f, "invalid_range"
		}
		until = t
	}
	if s := v.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return f, "invalid_range"
		}
		since = t
	} else if v.Get("until") != "" {
		since = until.Add(-defaultWindow)
	}
	if !until.After(since) || until.Sub(since) > maxWindow {
		return f, "invalid_range"
	}
	f.SinceMs, f.UntilMs = since.UTC().UnixMilli(), until.UTC().UnixMilli()
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			return f, "invalid_limit"
		}
		f.Limit = n
	}
	for _, k := range []string{"before", "after"} {
		if s := v.Get(k); s != "" && !ulidRE.MatchString(s) {
			return f, "invalid_cursor"
		}
	}
	f.Before, f.After = v.Get("before"), v.Get("after")
	if s := v.Get("kind"); s != "" {
		for _, k := range strings.Split(s, ",") {
			if !slices.Contains(analytics.AuditKinds, k) {
				return f, "invalid_kind"
			}
			f.Kinds = append(f.Kinds, k)
		}
	}
	f.Session, f.TraceID = v.Get("session"), v.Get("trace_id")
	if (f.Session != "" && !sessionRE.MatchString(f.Session)) || (f.TraceID != "" && !traceRE.MatchString(f.TraceID)) {
		return f, "invalid_filter"
	}
	f.Code, f.Class, f.Category = v.Get("code"), v.Get("class"), v.Get("category")
	for _, s := range []string{f.Code, f.Class, f.Category} {
		if s != "" && !codeRE.MatchString(s) {
			return f, "invalid_filter"
		}
	}
	f.Team, f.KeyID, f.Model, f.Provider = v.Get("team"), v.Get("key_id"), v.Get("model"), v.Get("provider")
	for _, s := range []string{f.Team, f.KeyID, f.Model, f.Provider} {
		if len(s) > maxFreeTextBytes {
			return f, "invalid_filter"
		}
	}
	f.PII, f.AutoOnly = v.Get("pii") == "true", v.Get("auto") == "true"
	return f, ""
}

func get(next func(w http.ResponseWriter, r *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			fail(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		next(w, r)
	})
}

// EventsHandler serves GET /admin/audit/events.
func EventsHandler(q analytics.AuditEventQuerier) http.Handler {
	return get(func(w http.ResponseWriter, r *http.Request) {
		f, code := ParseFilter(r.URL.Query(), time.Now(), listLimit)
		if code != "" {
			fail(w, http.StatusBadRequest, code)
			return
		}
		events, next, err := q.AuditEvents(f)
		if err != nil {
			storageFail(w, "events", err)
			return
		}
		if events == nil {
			events = []analytics.AuditEvent{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events, "next": next})
	})
}

// EventHandler serves GET /admin/audit/events/{id}.
func EventHandler(q analytics.AuditEventQuerier) http.Handler {
	return get(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !ulidRE.MatchString(id) {
			fail(w, http.StatusBadRequest, "invalid_id")
			return
		}
		d, ok, err := q.AuditEvent(id)
		switch {
		case err != nil:
			storageFail(w, "event", err)
		case !ok:
			fail(w, http.StatusNotFound, "not_found")
		default:
			writeJSON(w, http.StatusOK, d)
		}
	})
}

// SessionsHandler serves GET /admin/audit/sessions.
func SessionsHandler(q analytics.AuditEventQuerier) http.Handler {
	return get(func(w http.ResponseWriter, r *http.Request) {
		f, code := ParseFilter(r.URL.Query(), time.Now(), listLimit)
		if code != "" {
			fail(w, http.StatusBadRequest, code)
			return
		}
		sessions, next, err := q.AuditSessions(f)
		if err != nil {
			storageFail(w, "sessions", err)
			return
		}
		if sessions == nil {
			sessions = []analytics.AuditSession{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "next": next})
	})
}

// SessionHandler serves GET /admin/audit/sessions/{ref}; ref is the s1: digest.
func SessionHandler(q analytics.AuditEventQuerier) http.Handler {
	return get(func(w http.ResponseWriter, r *http.Request) {
		ref := r.PathValue("ref")
		if !sessionRE.MatchString(ref) {
			fail(w, http.StatusBadRequest, "invalid_id")
			return
		}
		f, code := ParseFilter(r.URL.Query(), time.Now(), sessionLimit)
		if code != "" {
			fail(w, http.StatusBadRequest, code)
			return
		}
		events, next, err := q.AuditSessionEvents(ref, f)
		if err != nil {
			storageFail(w, "session", err)
			return
		}
		if events == nil {
			events = []analytics.AuditEvent{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events, "next": next})
	})
}

// SummaryHandler serves GET /admin/audit/summary.
func SummaryHandler(q analytics.AuditEventQuerier) http.Handler {
	return get(func(w http.ResponseWriter, r *http.Request) {
		f, code := ParseFilter(r.URL.Query(), time.Now(), listLimit)
		if code != "" {
			fail(w, http.StatusBadRequest, code)
			return
		}
		s, err := q.AuditSummary(f)
		if err != nil {
			storageFail(w, "summary", err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
}
