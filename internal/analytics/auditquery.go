package analytics

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/inferplane/inferplane/internal/audit"
)

// Dialect differs only in placeholder syntax; the DDL uses portable types
// (SQLite accepts BIGINT with INTEGER affinity).
type Dialect struct{ Placeholder func(n int) string }

var (
	SQLiteDialect   = Dialect{Placeholder: func(int) string { return "?" }}
	PostgresDialect = Dialect{Placeholder: func(n int) string { return "$" + strconv.Itoa(n) }}
)

// AuditEventsDDL creates the console tables in either store (ADR-057).
const AuditEventsDDL = `
CREATE TABLE IF NOT EXISTS audit_events (
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
CREATE INDEX IF NOT EXISTS audit_events_ts ON audit_events(ts_ms);
CREATE INDEX IF NOT EXISTS audit_events_kind ON audit_events(kind, ts_ms);
CREATE INDEX IF NOT EXISTS audit_events_key ON audit_events(key_id, ts_ms);
CREATE INDEX IF NOT EXISTS audit_events_session ON audit_events(session_ref, ts_ms);
CREATE TABLE IF NOT EXISTS audit_event_categories (id TEXT NOT NULL, category TEXT NOT NULL, PRIMARY KEY (id, category));`

const auditColumns = `id, ts, ts_ms, event, kind, team, key_id, ingress, stream, status, deny_code, routing_reason, privacy, masked,
 would_restrict, pii, policy_ref, planned_boundary, actual_boundary, request_class, session_ref, trace_id, model_requested,
 model_actual, provider, fallback, attempt_count, auto_verdict, auto_stage, auto_signature, input_tokens, output_tokens,
 cost_micros, total_ms, ttft_ms, has_body, record_json`

const auditSelect = `SELECT e.id, e.ts, e.event, e.kind, e.team, e.key_id, e.ingress, e.stream, e.status, e.deny_code,
 e.routing_reason, e.privacy, e.masked, e.would_restrict, e.pii, e.policy_ref, e.planned_boundary, e.actual_boundary,
 e.request_class, e.session_ref, e.trace_id, e.model_requested, e.model_actual, e.provider, e.fallback, e.attempt_count,
 e.auto_verdict, e.auto_stage, e.auto_signature, e.input_tokens, e.output_tokens, e.cost_micros, e.total_ms, e.ttft_ms,
 e.has_body FROM audit_events e`

const defaultAuditLimit = 50

const deniedKinds = `('policy_denied','privacy_blocked')`

type RowScanner interface{ Scan(dest ...any) error }

// QueryFunc runs a query and calls each for every row.
type QueryFunc func(query string, args []any, each func(RowScanner) error) error

// ExecFunc runs one statement.
type ExecFunc func(query string, args ...any) error

type AuditFilter struct {
	Kinds                                                                 []string
	Team, KeyID, Model, Provider, Code, Category, Class, Session, TraceID string
	PII, AutoOnly                                                         bool
	SinceMs, UntilMs                                                      int64
	Limit                                                                 int
	Before, After                                                         string
}

type AuditEventDetail struct {
	Event     AuditEvent      `json:"event"`
	Record    json.RawMessage `json:"record"`
	Waterfall Waterfall       `json:"waterfall"`
}

type AuditSession struct {
	SessionRef  string `json:"session_ref"`
	Team        string `json:"team"`
	KeyID       string `json:"key_id"`
	FirstTS     string `json:"first_ts"`
	LastTS      string `json:"last_ts"`
	Requests    int64  `json:"requests"`
	CostMicros  int64  `json:"cost_micros"`
	Denials     int64  `json:"denials"`
	PII         int64  `json:"pii"`
	AutoBlocked int64  `json:"auto_blocked"`
	Subagents   int64  `json:"subagents"`
	lastID      string
}

type AuditCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}
type AuditPIICounts struct {
	Masked   int64 `json:"masked"`
	Internal int64 `json:"internal"`
	Blocked  int64 `json:"blocked"`
	Shadow   int64 `json:"shadow"`
}
type AuditDenial struct {
	Code   string `json:"code"`
	Policy string `json:"policy,omitempty"`
	Team   string `json:"team"`
	KeyID  string `json:"key_id"`
	Count  int64  `json:"count"`
	LastTS string `json:"last_ts"`
}
type AuditAutoCounts struct {
	Block      int64    `json:"block"`
	Allow      int64    `json:"allow"`
	Unknown    int64    `json:"unknown"`
	Signatures []string `json:"signatures"`
}
type AuditBucket struct {
	StartMs     int64 `json:"start_ms"`
	Requests    int64 `json:"requests"`
	Denials     int64 `json:"denials"`
	PII         int64 `json:"pii"`
	AutoBlocked int64 `json:"auto_blocked"`
}
type AuditSummary struct {
	SinceMs         int64            `json:"since_ms"`
	UntilMs         int64            `json:"until_ms"`
	ByKind          map[string]int64 `json:"by_kind"`
	Categories      []AuditCount     `json:"categories"`
	PII             AuditPIICounts   `json:"pii"`
	Denials         []AuditDenial    `json:"denials"`
	AutoMode        AuditAutoCounts  `json:"auto_mode"`
	Buckets         []AuditBucket    `json:"buckets"`
	CoverageStartMs int64            `json:"coverage_start_ms"`
}

// AuditEventQuerier is implemented by both analytics stores.
type AuditEventQuerier interface {
	AuditEvents(AuditFilter) ([]AuditEvent, string, error)
	AuditEvent(id string) (AuditEventDetail, bool, error)
	AuditSessions(AuditFilter) ([]AuditSession, string, error)
	AuditSessionEvents(ref string, f AuditFilter) ([]AuditEvent, string, error)
	AuditSummary(AuditFilter) (AuditSummary, error)
}

type sqlb struct {
	d     Dialect
	conds []string
	args  []any
}

func (b *sqlb) arg(v any) string { b.args = append(b.args, v); return b.d.Placeholder(len(b.args)) }

func (b *sqlb) where(cond string, v any) {
	b.conds = append(b.conds, strings.Replace(cond, "?", b.arg(v), 1))
}

func (b *sqlb) clause() string {
	if len(b.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(b.conds, " AND ")
}

func (b *sqlb) filter(f AuditFilter) {
	if f.SinceMs > 0 {
		b.where("e.ts_ms >= ?", f.SinceMs)
	}
	if f.UntilMs > 0 {
		b.where("e.ts_ms <= ?", f.UntilMs)
	}
	if len(f.Kinds) > 0 {
		ph := make([]string, len(f.Kinds))
		for i, k := range f.Kinds {
			ph[i] = b.arg(k)
		}
		b.conds = append(b.conds, "e.kind IN ("+strings.Join(ph, ", ")+")")
	}
	for col, v := range map[string]string{"e.team": f.Team, "e.key_id": f.KeyID, "e.provider": f.Provider, "e.deny_code": f.Code,
		"e.request_class": f.Class, "e.session_ref": f.Session, "e.trace_id": f.TraceID} {
		if v != "" {
			b.where(col+" = ?", v)
		}
	}
	if f.Model != "" {
		// One bind per placeholder: SQLite's "?" is positional.
		m1 := b.arg(f.Model)
		m2 := b.arg(f.Model)
		b.conds = append(b.conds, "(e.model_requested = "+m1+" OR e.model_actual = "+m2+")")
	}
	if f.Category != "" {
		b.where("EXISTS (SELECT 1 FROM audit_event_categories c WHERE c.id = e.id AND c.category = ?)", f.Category)
	}
	if f.PII {
		b.conds = append(b.conds, "e.pii = 1")
	}
	if f.AutoOnly {
		b.conds = append(b.conds, "e.auto_verdict <> ''")
	}
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

// InsertAuditEvent stores one row and its categories; a duplicate ID is ignored.
func InsertAuditEvent(d Dialect, exec ExecFunc, row AuditEventRow) error {
	e := row.AuditEvent
	vals := []any{e.ID, e.TS, row.TSMs, e.Event, e.Kind, e.Team, e.KeyID, e.Ingress, b2i(e.Stream), e.Status, e.DenyCode,
		e.RoutingReason, e.Privacy, b2i(e.Masked), b2i(e.WouldRestrict), b2i(e.PII), e.PolicyRef, e.PlannedBoundary,
		e.ActualBoundary, e.RequestClass, e.SessionRef, e.TraceID, e.ModelRequested, e.ModelActual, e.Provider,
		b2i(e.Fallback), e.AttemptCount, e.AutoVerdict, e.AutoStage, e.AutoSignature, e.InputTokens, e.OutputTokens,
		e.CostMicros, e.TotalMs, e.TTFTMs, b2i(e.HasBody), row.RecordJSON}
	ph := make([]string, len(vals))
	for i := range vals {
		ph[i] = d.Placeholder(i + 1)
	}
	if err := exec("INSERT INTO audit_events ("+auditColumns+") VALUES ("+strings.Join(ph, ", ")+") ON CONFLICT (id) DO NOTHING", vals...); err != nil {
		return err
	}
	for _, c := range e.Categories {
		if err := exec("INSERT INTO audit_event_categories (id, category) VALUES ("+d.Placeholder(1)+", "+d.Placeholder(2)+") ON CONFLICT (id, category) DO NOTHING", e.ID, c); err != nil {
			return err
		}
	}
	return nil
}

func scanAuditEvent(s RowScanner) (AuditEvent, error) {
	var e AuditEvent
	var stream, masked, would, pii, fallback, hasBody int64
	err := s.Scan(&e.ID, &e.TS, &e.Event, &e.Kind, &e.Team, &e.KeyID, &e.Ingress, &stream, &e.Status, &e.DenyCode,
		&e.RoutingReason, &e.Privacy, &masked, &would, &pii, &e.PolicyRef, &e.PlannedBoundary, &e.ActualBoundary,
		&e.RequestClass, &e.SessionRef, &e.TraceID, &e.ModelRequested, &e.ModelActual, &e.Provider, &fallback,
		&e.AttemptCount, &e.AutoVerdict, &e.AutoStage, &e.AutoSignature, &e.InputTokens, &e.OutputTokens, &e.CostMicros,
		&e.TotalMs, &e.TTFTMs, &hasBody)
	e.Stream, e.Masked, e.WouldRestrict, e.PII, e.Fallback, e.HasBody = stream == 1, masked == 1, would == 1, pii == 1, fallback == 1, hasBody == 1
	return e, err
}

func attachCategories(d Dialect, run QueryFunc, events []AuditEvent) error {
	if len(events) == 0 {
		return nil
	}
	b := &sqlb{d: d}
	ph := make([]string, len(events))
	idx := map[string]int{}
	for i, e := range events {
		ph[i] = b.arg(e.ID)
		idx[e.ID] = i
	}
	return run("SELECT id, category FROM audit_event_categories WHERE id IN ("+strings.Join(ph, ", ")+") ORDER BY category", b.args,
		func(s RowScanner) error {
			var id, c string
			if err := s.Scan(&id, &c); err != nil {
				return err
			}
			events[idx[id]].Categories = append(events[idx[id]].Categories, c)
			return nil
		})
}

func listEvents(d Dialect, run QueryFunc, b *sqlb, order string, limit int) ([]AuditEvent, string, error) {
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	var out []AuditEvent
	// Compute the LIMIT placeholder first: evaluation order of b.args versus
	// b.arg() inside one call expression is unspecified.
	lim := b.arg(limit + 1)
	err := run(auditSelect+b.clause()+" ORDER BY e.id "+order+" LIMIT "+lim, b.args, func(s RowScanner) error {
		e, err := scanAuditEvent(s)
		out = append(out, e)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, attachCategories(d, run, out)
}

func RunAuditEvents(d Dialect, run QueryFunc, f AuditFilter) ([]AuditEvent, string, error) {
	b := &sqlb{d: d}
	b.filter(f)
	if f.Before != "" {
		b.where("e.id < ?", f.Before)
	}
	return listEvents(d, run, b, "DESC", f.Limit)
}

func RunAuditSessionEvents(d Dialect, run QueryFunc, ref string, f AuditFilter) ([]AuditEvent, string, error) {
	b := &sqlb{d: d}
	b.where("e.session_ref = ?", ref)
	if f.After != "" {
		b.where("e.id > ?", f.After)
	}
	return listEvents(d, run, b, "ASC", f.Limit)
}

func RunAuditEvent(d Dialect, run QueryFunc, id string) (AuditEventDetail, bool, error) {
	b := &sqlb{d: d}
	b.where("e.id = ?", id)
	var detail AuditEventDetail
	found := false
	err := run(strings.Replace(auditSelect, " FROM audit_events e", ", e.record_json FROM audit_events e", 1)+b.clause(), b.args, func(s RowScanner) error {
		var e AuditEvent
		var stream, masked, would, pii, fallback, hasBody int64
		var raw string
		if err := s.Scan(&e.ID, &e.TS, &e.Event, &e.Kind, &e.Team, &e.KeyID, &e.Ingress, &stream, &e.Status, &e.DenyCode,
			&e.RoutingReason, &e.Privacy, &masked, &would, &pii, &e.PolicyRef, &e.PlannedBoundary, &e.ActualBoundary,
			&e.RequestClass, &e.SessionRef, &e.TraceID, &e.ModelRequested, &e.ModelActual, &e.Provider, &fallback,
			&e.AttemptCount, &e.AutoVerdict, &e.AutoStage, &e.AutoSignature, &e.InputTokens, &e.OutputTokens, &e.CostMicros,
			&e.TotalMs, &e.TTFTMs, &hasBody, &raw); err != nil {
			return err
		}
		e.Stream, e.Masked, e.WouldRestrict, e.PII, e.Fallback, e.HasBody = stream == 1, masked == 1, would == 1, pii == 1, fallback == 1, hasBody == 1
		detail.Event, detail.Record, found = e, json.RawMessage(raw), true
		var rec audit.Record
		if json.Unmarshal([]byte(raw), &rec) == nil {
			detail.Waterfall = WaterfallOf(rec)
		}
		return nil
	})
	if err != nil || !found {
		return AuditEventDetail{}, false, err
	}
	events := []AuditEvent{detail.Event}
	if err := attachCategories(d, run, events); err != nil {
		return AuditEventDetail{}, false, err
	}
	detail.Event = events[0]
	return detail, true, nil
}

func RunAuditSessions(d Dialect, run QueryFunc, f AuditFilter) ([]AuditSession, string, error) {
	b := &sqlb{d: d}
	b.filter(f)
	b.conds = append(b.conds, "e.session_ref <> ''")
	q := `SELECT e.session_ref, e.team, e.key_id, MIN(e.ts), MAX(e.ts), COUNT(*), SUM(e.cost_micros),
	 SUM(CASE WHEN e.kind IN ` + deniedKinds + ` THEN 1 ELSE 0 END), SUM(e.pii),
	 SUM(CASE WHEN e.kind = 'auto_mode_blocked' THEN 1 ELSE 0 END),
	 SUM(CASE WHEN e.request_class = 'subagent' THEN 1 ELSE 0 END), MAX(e.id) FROM audit_events e` + b.clause() +
		" GROUP BY e.session_ref, e.team, e.key_id"
	if f.Before != "" {
		q += " HAVING MAX(e.id) < " + b.arg(f.Before)
	}
	if f.Limit <= 0 {
		f.Limit = defaultAuditLimit
	}
	lim := b.arg(f.Limit + 1)
	q += " ORDER BY MAX(e.id) DESC LIMIT " + lim
	var out []AuditSession
	err := run(q, b.args, func(s RowScanner) error {
		var x AuditSession
		err := s.Scan(&x.SessionRef, &x.Team, &x.KeyID, &x.FirstTS, &x.LastTS, &x.Requests, &x.CostMicros, &x.Denials, &x.PII, &x.AutoBlocked, &x.Subagents, &x.lastID)
		out = append(out, x)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = out[f.Limit-1].lastID
	}
	return out, next, nil
}

func RunAuditSummary(d Dialect, run QueryFunc, f AuditFilter) (AuditSummary, error) {
	sum := AuditSummary{SinceMs: f.SinceMs, UntilMs: f.UntilMs, ByKind: map[string]int64{}, Categories: []AuditCount{},
		Denials: []AuditDenial{}, AutoMode: AuditAutoCounts{Signatures: []string{}}, Buckets: []AuditBucket{}}
	where := func() *sqlb { b := &sqlb{d: d}; b.filter(f); return b }
	b := where()
	if err := run("SELECT e.kind, COUNT(*) FROM audit_events e"+b.clause()+" GROUP BY e.kind", b.args, func(s RowScanner) error {
		var k string
		var n int64
		err := s.Scan(&k, &n)
		sum.ByKind[k] = n
		return err
	}); err != nil {
		return sum, err
	}
	sum.PII = AuditPIICounts{Masked: sum.ByKind[KindPrivacyMasked], Internal: sum.ByKind[KindPrivacyInternal], Blocked: sum.ByKind[KindPrivacyBlocked]}
	b = where()
	if err := run("SELECT c.category, COUNT(*) FROM audit_event_categories c JOIN audit_events e ON e.id = c.id"+b.clause()+
		" GROUP BY c.category ORDER BY COUNT(*) DESC, c.category LIMIT 10", b.args, func(s RowScanner) error {
		var c AuditCount
		err := s.Scan(&c.Name, &c.Count)
		sum.Categories = append(sum.Categories, c)
		return err
	}); err != nil {
		return sum, err
	}
	b = where()
	b.conds = append(b.conds, "e.would_restrict = 1", "e.kind = 'ok'")
	if err := run("SELECT COUNT(*) FROM audit_events e"+b.clause(), b.args, func(s RowScanner) error { return s.Scan(&sum.PII.Shadow) }); err != nil {
		return sum, err
	}
	b = where()
	b.conds = append(b.conds, "e.kind IN "+deniedKinds)
	if err := run("SELECT e.deny_code, e.policy_ref, e.team, e.key_id, COUNT(*), MAX(e.ts) FROM audit_events e"+b.clause()+
		" GROUP BY e.deny_code, e.policy_ref, e.team, e.key_id ORDER BY COUNT(*) DESC, MAX(e.ts) DESC, e.deny_code, e.policy_ref, e.team, e.key_id LIMIT 50", b.args, func(s RowScanner) error {
		var x AuditDenial
		err := s.Scan(&x.Code, &x.Policy, &x.Team, &x.KeyID, &x.Count, &x.LastTS)
		sum.Denials = append(sum.Denials, x)
		return err
	}); err != nil {
		return sum, err
	}
	b = where()
	b.conds = append(b.conds, "e.auto_verdict <> ''")
	if err := run("SELECT e.auto_verdict, e.auto_signature, COUNT(*) FROM audit_events e"+b.clause()+" GROUP BY e.auto_verdict, e.auto_signature", b.args,
		func(s RowScanner) error {
			var v, sig string
			var n int64
			if err := s.Scan(&v, &sig, &n); err != nil {
				return err
			}
			switch v {
			case "block":
				sum.AutoMode.Block += n
			case "allow":
				sum.AutoMode.Allow += n
			default:
				sum.AutoMode.Unknown += n
			}
			if sig != "" && !slicesContains(sum.AutoMode.Signatures, sig) {
				sum.AutoMode.Signatures = append(sum.AutoMode.Signatures, sig)
			}
			return nil
		}); err != nil {
		return sum, err
	}
	slices.Sort(sum.AutoMode.Signatures)
	if f.SinceMs > 0 && f.UntilMs > f.SinceMs {
		width := max((f.UntilMs-f.SinceMs)/12, 1)
		sum.Buckets = make([]AuditBucket, 12)
		for i := range sum.Buckets {
			sum.Buckets[i].StartMs = f.SinceMs + int64(i)*width
		}
		b = &sqlb{d: d}
		s1, s2 := b.arg(f.SinceMs), b.arg(width)
		b.filter(f)
		if err := run("SELECT (e.ts_ms - "+s1+") / "+s2+", COUNT(*), SUM(CASE WHEN e.kind IN "+deniedKinds+" THEN 1 ELSE 0 END), SUM(e.pii),"+
			" SUM(CASE WHEN e.kind = 'auto_mode_blocked' THEN 1 ELSE 0 END) FROM audit_events e"+b.clause()+" GROUP BY 1", b.args,
			func(s RowScanner) error {
				var i int64
				var x AuditBucket
				if err := s.Scan(&i, &x.Requests, &x.Denials, &x.PII, &x.AutoBlocked); err != nil {
					return err
				}
				i = min(max(i, 0), 11)
				bk := &sum.Buckets[i]
				bk.Requests, bk.Denials, bk.PII, bk.AutoBlocked = bk.Requests+x.Requests, bk.Denials+x.Denials, bk.PII+x.PII, bk.AutoBlocked+x.AutoBlocked
				return nil
			}); err != nil {
			return sum, err
		}
	}
	err := run("SELECT COALESCE(MIN(ts_ms), 0) FROM audit_events", nil, func(s RowScanner) error { return s.Scan(&sum.CoverageStartMs) })
	return sum, err
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
