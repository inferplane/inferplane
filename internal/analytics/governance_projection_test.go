package analytics

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
)

// legacyAuditEventsTable is the console table as first shipped, before the
// ADR-057 governance columns.
const legacyAuditEventsTable = `CREATE TABLE audit_events (
  id TEXT PRIMARY KEY, ts TEXT NOT NULL DEFAULT '', ts_ms BIGINT NOT NULL DEFAULT 0, event TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '', team TEXT NOT NULL DEFAULT '', key_id TEXT NOT NULL DEFAULT '', ingress TEXT NOT NULL DEFAULT '',
  stream INTEGER NOT NULL DEFAULT 0, status INTEGER NOT NULL DEFAULT 0, deny_code TEXT NOT NULL DEFAULT '',
  routing_reason TEXT NOT NULL DEFAULT '', privacy TEXT NOT NULL DEFAULT '', masked INTEGER NOT NULL DEFAULT 0,
  would_restrict INTEGER NOT NULL DEFAULT 0, pii INTEGER NOT NULL DEFAULT 0, policy_ref TEXT NOT NULL DEFAULT '',
  planned_boundary TEXT NOT NULL DEFAULT '', actual_boundary TEXT NOT NULL DEFAULT '', request_class TEXT NOT NULL DEFAULT '',
  session_ref TEXT NOT NULL DEFAULT '', trace_id TEXT NOT NULL DEFAULT '', model_requested TEXT NOT NULL DEFAULT '',
  model_actual TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', fallback INTEGER NOT NULL DEFAULT 0,
  attempt_count INTEGER NOT NULL DEFAULT 0, auto_verdict TEXT NOT NULL DEFAULT '', auto_stage TEXT NOT NULL DEFAULT '',
  auto_signature TEXT NOT NULL DEFAULT '', input_tokens BIGINT NOT NULL DEFAULT 0, output_tokens BIGINT NOT NULL DEFAULT 0,
  cost_micros BIGINT NOT NULL DEFAULT 0, total_ms BIGINT NOT NULL DEFAULT 0, ttft_ms BIGINT NOT NULL DEFAULT 0,
  has_body INTEGER NOT NULL DEFAULT 0, record_json TEXT NOT NULL DEFAULT ''
);
CREATE TABLE audit_event_categories (id TEXT NOT NULL, category TEXT NOT NULL, PRIMARY KEY (id, category));`

// governanceDenial is a 429 whose record names a policy rule; the request
// also matched a privacy rule that did not refuse it.
func governanceDenial(id string) audit.Record {
	return rec(id, "request_started", 429, func(r *audit.Record) {
		r.Outcome.Error = str("team_rate_limited")
		r.Request.Routing = &audit.RoutingRef{Reason: "unchanged", Policies: []audit.RoutingPolicyRef{{Name: "privacy", Generation: 7, Rule: "protect"}}}
		r.Governance = &audit.GovernanceRef{Scope: audit.GovScopeTeam, Limit: audit.GovLimitRPM, Source: audit.GovSourcePolicy,
			Policy: &audit.PolicyRuleRef{Name: "team-rate", Generation: 2, Rule: "rpm"}}
	})
}

func TestProjectGovernanceDenial(t *testing.T) {
	row, ok := ProjectAuditEvent(governanceDenial("01G"), nil)
	if !ok || row.GovScope != "team" || row.GovLimit != "rpm" || row.GovSource != "policy" || row.PolicyRef != "team-rate#2·rpm" {
		t.Fatalf("row = %+v", row.AuditEvent)
	}
	authority := governanceDenial("01H")
	authority.Governance.Policy.Generation = 0
	if row, _ := ProjectAuditEvent(authority, nil); row.PolicyRef != "team-rate·rpm" {
		t.Fatalf("generation-less rule = %q", row.PolicyRef)
	}
	setting := governanceDenial("01S")
	setting.Governance = &audit.GovernanceRef{Scope: audit.GovScopeKey, Limit: audit.GovLimitRPM, Source: audit.GovSourceKey}
	if row, _ := ProjectAuditEvent(setting, nil); row.PolicyRef != "" || row.GovSource != "key" || row.GovScope != "key" {
		t.Fatalf("setting denial = %+v", row.AuditEvent)
	}
	// A denial written before the field, which also matched a privacy rule:
	// no source, and the privacy rule is not named as the refuser.
	legacy := rec("01L", "request_started", 429, func(r *audit.Record) {
		r.Outcome.Error = str("key_rate_limited")
		r.Request.Routing = &audit.RoutingRef{Reason: "unchanged", Policies: []audit.RoutingPolicyRef{{Name: "privacy", Generation: 7, Rule: "protect"}}}
	})
	if row, _ := ProjectAuditEvent(legacy, nil); row.GovScope != "" || row.GovLimit != "" || row.GovSource != "" || row.PolicyRef != "" {
		t.Fatalf("legacy denial = %+v", row.AuditEvent)
	}
	// A routing denial still names its routing rule.
	blocked := rec("01B", "request_started", 403, func(r *audit.Record) {
		r.Request.Routing = &audit.RoutingRef{Reason: "sensitive_blocked", Privacy: "block", Policies: []audit.RoutingPolicyRef{{Name: "privacy", Generation: 7, Rule: "protect"}}}
	})
	if row, _ := ProjectAuditEvent(blocked, nil); row.PolicyRef != "privacy#7·protect" {
		t.Fatalf("routing denial policy = %q", row.PolicyRef)
	}
}

// A console table created before the governance columns gains them; its rows
// stay, read as "not recorded", and boot replay projects new records into it.
func TestAuditEventsMigrationKeepsRowsAndReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := rec("01JC0000000000000000000041", "request_started", 429, func(r *audit.Record) { r.Outcome.Error = str("key_rate_limited") })
	oldLine, err := old.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(legacyAuditEventsTable); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_events(id, ts, ts_ms, event, kind, team, key_id, status, deny_code, record_json)
		VALUES (?, '2026-10-29T01:00:00Z', 1793235600000, 'request_started', 'policy_denied', 'platform', 'ik_a', 429, 'key_rate_limited', ?)`,
		old.ID, string(oldLine)); err != nil {
		t.Fatal(err)
	}
	// Rows the earlier release projected with the first routing rule as
	// policy_ref: one on a governance-coded denial (the rule did not refuse
	// it) and one on a routing denial (it did).
	for id, code := range map[string]string{"01JC0000000000000000000043": "key_rate_limited", "01JC0000000000000000000044": "sensitive_blocked"} {
		if _, err := db.Exec(`INSERT INTO audit_events(id, ts, ts_ms, event, kind, team, key_id, status, deny_code, policy_ref, record_json)
			VALUES (?, '2026-10-29T01:00:01Z', 1793235601000, 'request_started', 'policy_denied', 'platform', 'ik_a', 403, ?, 'privacy#7·protect', '{}')`, id, code); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for open := 0; open < 2; open++ { // the second open re-runs an idempotent migration
		ix, err := OpenSQLite(path)
		if err != nil {
			t.Fatalf("open %d: %v", open, err)
		}
		newer := governanceDenial("01JC0000000000000000000042")
		line, err := newer.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ix.Replay(strings.NewReader(string(oldLine) + "\n" + string(line) + "\n")); err != nil {
			t.Fatal(err)
		}
		kept, ok, err := ix.AuditEvent(old.ID)
		if err != nil || !ok || kept.Event.DenyCode != "key_rate_limited" || kept.Event.GovSource != "" || kept.Event.PolicyRef != "" {
			t.Fatalf("open %d: legacy row = %+v ok=%v err=%v", open, kept.Event, ok, err)
		}
		stale, _, err := ix.AuditEvent("01JC0000000000000000000043")
		if err != nil || stale.Event.PolicyRef != "" {
			t.Fatalf("open %d: stale routing rule on governance denial = %q err=%v", open, stale.Event.PolicyRef, err)
		}
		routed, _, err := ix.AuditEvent("01JC0000000000000000000044")
		if err != nil || routed.Event.PolicyRef != "privacy#7·protect" {
			t.Fatalf("open %d: routing denial rule = %q err=%v", open, routed.Event.PolicyRef, err)
		}
		sum, err := ix.AuditSummary(AuditFilter{})
		if err != nil || len(sum.Denials) != 3 {
			t.Fatalf("open %d: denials = %+v err=%v", open, sum.Denials, err)
		}
		for _, d := range sum.Denials {
			switch d.Code {
			case "team_rate_limited":
				if d.Policy != "team-rate#2·rpm" || d.Scope != "team" || d.Limit != "rpm" || d.Source != "policy" {
					t.Fatalf("open %d: governance denial = %+v", open, d)
				}
			case "sensitive_blocked":
				if d.Policy != "privacy#7·protect" {
					t.Fatalf("open %d: routing denial = %+v", open, d)
				}
			case "key_rate_limited":
				if d.Policy != "" || d.Scope != "" || d.Limit != "" || d.Source != "" {
					t.Fatalf("open %d: legacy denial = %+v", open, d)
				}
			default:
				t.Fatalf("open %d: unexpected denial %+v", open, d)
			}
		}
		if err := ix.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
