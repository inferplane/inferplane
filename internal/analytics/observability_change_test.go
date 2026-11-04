package analytics

import (
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
)

// TestObservabilityChangeRecordsStayOutOfTheConsole pins that the admin_observability_changed event name stays out of the console and analytics projections (they key on Event), not the change field itself.
func TestObservabilityChangeRecordsStayOutOfTheConsole(t *testing.T) {
	rec := audit.Record{SchemaVersion: 1, Event: "admin_observability_changed", ID: "01JC0000000000000000000001", TS: "2026-10-29T10:00:00Z",
		Request: audit.RequestRef{Ingress: "admin", Change: &audit.ChangeRef{Target: "observability_limits", Before: "sha256:aa", After: "sha256:bb", Names: []string{"corp"}}}}
	if AuditObservable(rec) || Observable(rec) {
		t.Fatal("an admin change record reached the audit console or the analytics projection")
	}
}
