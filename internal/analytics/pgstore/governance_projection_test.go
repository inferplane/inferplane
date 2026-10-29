package pgstore

import (
	"context"
	"testing"

	"github.com/inferplane/inferplane/internal/analytics"
	"github.com/inferplane/inferplane/internal/audit"
)

// A Mode B console table created before the governance columns gains them
// once (migration marker 2); its rows stay and read as "not recorded", and
// later ticks fill the columns from new records (ADR-057).
func TestPgstoreAuditEventsMigrationKeepsRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	epoch := acquire(t, s)
	old := auditRec("01JC0000000000000000000061", "request_started", 429, func(r *audit.Record) { r.Outcome.Error = auditStr("key_rate_limited") })
	if err := s.ingestBatch(ctx, "holder", epoch, []audit.Record{old}, nil); err != nil {
		t.Fatal(err)
	}
	// Recreate a database from before the governance columns.
	if _, err := s.db.Exec(ctx, `ALTER TABLE audit_events DROP COLUMN gov_scope, DROP COLUMN gov_limit, DROP COLUMN gov_source;
DELETE FROM analytics_schema_migrations WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	for migration := 0; migration < 2; migration++ {
		if err := s.ensureSchema(ctx); err != nil {
			t.Fatalf("migration %d: %v", migration, err)
		}
	}
	var markers int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM analytics_schema_migrations WHERE version = 2`).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("markers = %d err=%v", markers, err)
	}
	kept, ok, err := s.AuditEvent(old.ID)
	if err != nil || !ok || kept.Event.DenyCode != "key_rate_limited" || kept.Event.GovSource != "" {
		t.Fatalf("legacy row = %+v ok=%v err=%v", kept.Event, ok, err)
	}
	newer := auditRec("01JC0000000000000000000062", "request_started", 402, func(r *audit.Record) {
		r.Outcome.Error = auditStr("team_budget_exceeded")
		r.Governance = &audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitBudgetMonth, Source: audit.GovSourcePolicy,
			Policy: &audit.PolicyRuleRef{Name: "money", Generation: 3, Rule: "month"}}
	})
	if err := s.ingestBatch(ctx, "holder", epoch, []audit.Record{newer}, nil); err != nil {
		t.Fatal(err)
	}
	sum, err := s.AuditSummary(analytics.AuditFilter{})
	if err != nil || len(sum.Denials) != 2 {
		t.Fatalf("denials = %+v err=%v", sum.Denials, err)
	}
	for _, d := range sum.Denials {
		switch d.Code {
		case "team_budget_exceeded":
			if d.Policy != "money#3·month" || d.Scope != "team" || d.Limit != "budget_month" || d.Source != "policy" {
				t.Fatalf("governance denial = %+v", d)
			}
		case "key_rate_limited":
			if d.Policy != "" || d.Scope != "" || d.Limit != "" || d.Source != "" {
				t.Fatalf("legacy denial = %+v", d)
			}
		default:
			t.Fatalf("unexpected denial %+v", d)
		}
	}
}
