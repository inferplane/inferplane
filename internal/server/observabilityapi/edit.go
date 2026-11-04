package observabilityapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/pkg/ulid"
)

// apiError is the JSON error body: a human message (what the console's api()
// helper shows) and a stable code the console maps to translated text.
type apiError struct {
	Error           string `json:"error"`
	Code            string `json:"code"`
	Reason          string `json:"reason,omitempty"`
	CurrentRevision string `json:"current_revision,omitempty"`
}

func writeError(w http.ResponseWriter, status int, e apiError) { writeJSON(w, status, e) }

// decode reads one strict JSON object of at most max bytes into v: an unknown
// field (an endpoint, a header) is refused, never ignored.
func decode(w http.ResponseWriter, r *http.Request, max int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, apiError{Error: "request body too large", Code: "invalid_request"})
			return false
		}
		writeError(w, http.StatusBadRequest, apiError{Error: "the body must be one JSON object with known fields: " + err.Error(), Code: "invalid_request"})
		return false
	}
	if err := dec.Decode(&json.RawMessage{}); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, apiError{Error: "request body too large", Code: "invalid_request"})
			return false
		}
		writeError(w, http.StatusBadRequest, apiError{Error: "the body has data after the JSON object", Code: "invalid_request"})
		return false
	}
	return true
}

// checkRevision enforces the optimistic precondition: an apply must name the
// revision its preview saw; a dry run may omit it.
func checkRevision(w http.ResponseWriter, dryRun bool, expected, current string) bool {
	if expected == "" && !dryRun {
		writeError(w, http.StatusBadRequest, apiError{Error: "expected_revision is required to apply; run a dry run first", Code: "invalid_request"})
		return false
	}
	if expected != "" && expected != current {
		writeError(w, http.StatusConflict, apiError{Error: "the state changed since it was read; reload and preview again", Code: "revision_conflict", CurrentRevision: current})
		return false
	}
	return true
}

// auditReady refuses an apply while the audit chain is not accepting records:
// a change that cannot be recorded is not made.
func (c *Console) auditReady(w http.ResponseWriter) bool {
	if c.opts.AuditReady == nil {
		return true
	}
	if ok, _ := c.opts.AuditReady(); ok {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, apiError{Error: "the audit chain is not accepting records, so nothing was changed", Code: "audit_unavailable"})
	return false
}

// emitChange appends the admin_observability_changed record of one applied
// edit: the administrator from the request context and the revisions before
// and after. No state content enters the chain.
func (c *Console) emitChange(r *http.Request, target, before, after string, names []string) {
	if c.opts.Emit == nil {
		return
	}
	rec := audit.Record{
		SchemaVersion: 1,
		Event:         "admin_observability_changed",
		ID:            ulid.New(),
		TS:            time.Now().UTC().Format(time.RFC3339Nano),
		Request:       audit.RequestRef{Ingress: "admin", Change: &audit.ChangeRef{Target: target, Before: before, After: after, Names: names}},
	}
	if id, ok := principal.AdminFrom(r.Context()); ok {
		sub, method := id.Subject, id.AuthMethod
		rec.Principal = audit.PrincipalRef{User: &sub, AuthMethod: &method}
	}
	c.opts.Emit(rec)
}

// writeFileAtomic replaces path with data: a 0600 temp file in the same
// directory (extension .tmp, so a policy directory never loads it), fsync,
// rename, then a best-effort directory fsync. Readers see the old bytes or the
// new ones, never a torn file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
