// Package maskingapi exposes the local optional masking switch to full admins.
package maskingapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/filter"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/pkg/ulid"
)

type Handler struct {
	mu      sync.Mutex
	control *filter.Control
	aud     *audit.Writer
	org     func(context.Context) (string, error)
}

func New(c *filter.Control, aud *audit.Writer, org func(context.Context) (string, error)) *Handler {
	if c == nil {
		c, _ = filter.OpenControl("", false)
	}
	return &Handler{control: c, aud: aud, org: org}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	id, ok := principal.AdminFrom(r.Context())
	if !ok || !id.IsAdmin {
		http.Error(w, `{"error":"full administrator required"}`, 403)
		return
	}
	switch r.Method {
	case "GET":
		_ = json.NewEncoder(w).Encode(h.control.Snapshot())
	case "PUT":
		h.mu.Lock()
		defer h.mu.Unlock()
		var body struct {
			Enabled  *bool  `json:"enabled"`
			Expected string `json:"expected_revision"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil || body.Enabled == nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, `{"error":"enabled and expected_revision are required"}`, 400)
			return
		}
		org := ""
		if h.org != nil {
			var err error
			org, err = h.org(r.Context())
			if err != nil {
				http.Error(w, `{"error":"audit identity unavailable"}`, 503)
				return
			}
		}
		if ready, _ := h.aud.Ready(); !ready {
			http.Error(w, `{"error":"audit unavailable"}`, 503)
			return
		}
		if err := h.control.Set(*body.Enabled, body.Expected); err != nil {
			code := 503
			if errors.Is(err, filter.ErrReadOnly) {
				code = 405
			} else if errors.Is(err, filter.ErrConflict) {
				code = 409
			}
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		state := h.control.Snapshot()
		if h.aud != nil {
			h.aud.Append(audit.Record{
				SchemaVersion: 1, Event: "admin_masking_changed", ID: ulid.New(),
				TS: time.Now().UTC().Format(time.RFC3339Nano), Principal: principal.AdminAuditRef(id, org),
				Request: audit.RequestRef{Ingress: "admin", Change: &audit.ChangeRef{
					Target: "optional_masking", Before: body.Expected, After: state.Revision,
				}},
			})
		}
		_ = json.NewEncoder(w).Encode(state)
	default:
		w.Header().Set("Allow", "GET, PUT")
		w.WriteHeader(405)
	}
}
