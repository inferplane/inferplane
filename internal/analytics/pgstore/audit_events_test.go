package pgstore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/analytics"
	"github.com/inferplane/inferplane/internal/audit"
)

func auditRec(id, event string, status int, mutate func(*audit.Record)) audit.Record {
	r := audit.Record{SchemaVersion: 1, Event: event, ID: id, TS: "2026-10-29T01:00:00Z",
		Principal: audit.PrincipalRef{KeyID: "ik_a", Team: "platform"}, Request: audit.RequestRef{Ingress: "anthropic", ModelRequested: "claude-sonnet-5"},
		Outcome: &audit.OutcomeRef{Status: status}}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func auditStr(s string) *string { return &s }

func auditSession(i int) string { return "s1:" + fmt.Sprintf("%032d", i) }

// auditSeed mirrors internal/analytics seedAuditRows: 30 rows, timestamps tied
// in groups of five, i%3 selects the auto verdict, i%7==0 is a 429 denial.
func auditSeed(base time.Time) []audit.Record {
	var out []audit.Record
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("01JC00000000000000000001%02d", i)
		ts := base.Add(time.Duration(i/5) * time.Hour).Format(time.RFC3339Nano)
		out = append(out, auditRec(id, "request_completed", 200, func(r *audit.Record) {
			r.TS = ts
			r.Principal.Team = []string{"platform", "data"}[i%2]
			r.Request.Provider = []string{"anthropic", "bedrock"}[i%2]
			r.Request.Routing = &audit.RoutingRef{RequestedModel: "sonnet", ActualModel: "glm", Masked: i%3 == 0, Categories: []string{"email"}}
			r.Client = &audit.ClientRef{RequestClass: "main", SessionRef: auditSession(i % 4)}
			r.TraceID = auditStr("trace1")
			r.AutoMode = &audit.AutoModeRef{Verdict: []string{"allow", "block", "unknown"}[i%3], Stage: "s1", Signature: "claude-code-2.1"}
			r.Cost = &audit.CostRef{AmountUSDMicros: int64(1000 + i)}
			if i%7 == 0 {
				r.Event = "request_started"
				r.Outcome.Status = 429
				r.Outcome.Error = auditStr("key_rate_limited")
			}
		}))
	}
	return out
}

func acquire(t *testing.T, s *Store) int64 {
	t.Helper()
	epoch, ok, err := tryAcquireLease(context.Background(), s.db, "holder", 15*time.Second)
	if err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	return epoch
}

func auditCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPgstoreAuditEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	epoch := acquire(t, s)
	base := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	if err := s.ingestBatch(ctx, "holder", epoch, auditSeed(base), map[string]int64{}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	since, until := base.UnixMilli(), base.Add(6*time.Hour).UnixMilli()

	// Events: every filter, paged by cursor.
	full := analytics.AuditFilter{Kinds: []string{analytics.KindOK, analytics.KindPrivacyMasked, analytics.KindAutoModeBlocked, analytics.KindPolicyDenied},
		Team: "platform", KeyID: "ik_a", Model: "sonnet", Provider: "anthropic", Category: "email", Class: "main", TraceID: "trace1",
		PII: true, AutoOnly: true, SinceMs: since, UntilMs: until, Limit: 3}
	var ids []string
	cur := ""
	for page := 0; page < 20; page++ {
		f := full
		f.Before = cur
		got, next, err := s.AuditEvents(f)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, e := range got {
			ids = append(ids, e.ID)
		}
		if next == "" {
			break
		}
		cur = next
	}
	var want []string
	for i := 28; i >= 0; i -= 2 {
		if i%3 == 0 {
			continue
		}
		want = append(want, fmt.Sprintf("01JC00000000000000000001%02d", i))
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("paged ids = %v\nwant %v", ids, want)
	}

	// AutoOnly: block/unknown listed, allow excluded.
	auto, _, err := s.AuditEvents(analytics.AuditFilter{AutoOnly: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range auto {
		seen[e.AutoVerdict]++
	}
	if seen["allow"] != 0 || seen["block"] != 10 || seen["unknown"] != 10 || len(auto) != 20 {
		t.Fatalf("auto-only = %v rows %d", seen, len(auto))
	}

	// Code + session, default limit.
	cs, _, err := s.AuditEvents(analytics.AuditFilter{Code: "key_rate_limited", Session: auditSession(0)})
	if err != nil || len(cs) != 2 || cs[0].ID != "01JC0000000000000000000128" || cs[1].ID != "01JC0000000000000000000100" || cs[1].Kind != analytics.KindPolicyDenied {
		t.Fatalf("code+session = %+v err=%v", cs, err)
	}
	all, next, err := s.AuditEvents(analytics.AuditFilter{})
	if err != nil || len(all) != 30 || next != "" {
		t.Fatalf("zero limit = %d next=%q err=%v", len(all), next, err)
	}

	// Detail.
	d, ok, err := s.AuditEvent("01JC0000000000000000000105")
	if err != nil || !ok || d.Event.ID != "01JC0000000000000000000105" || len(d.Record) == 0 || d.Hash == "" {
		t.Fatalf("detail = %+v ok=%v err=%v", d, ok, err)
	}
	if _, ok, err := s.AuditEvent("missing"); err != nil || ok {
		t.Fatalf("missing detail ok=%v err=%v", ok, err)
	}

	// Sessions with HAVING cursor (SUM(cost) etc. must scan on Postgres).
	sf := analytics.AuditFilter{Team: "platform", Model: "sonnet", Category: "email", PII: true, SinceMs: since, UntilMs: until, Limit: 1}
	var refs []string
	cur = ""
	for page := 0; page < 10; page++ {
		g := sf
		g.Before = cur
		got, next, err := s.AuditSessions(g)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range got {
			refs = append(refs, x.SessionRef)
			if x.CostMicros <= 0 || x.Requests <= 0 {
				t.Fatalf("session aggregates not scanned: %+v", x)
			}
		}
		if next == "" {
			break
		}
		cur = next
	}
	if !slices.Equal(refs, []string{auditSession(0), auditSession(2)}) {
		t.Fatalf("sessions = %v", refs)
	}

	// Session events with After cursor.
	var sids []string
	cur = ""
	for {
		got, next, err := s.AuditSessionEvents(auditSession(1), analytics.AuditFilter{Limit: 2, After: cur})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range got {
			sids = append(sids, e.ID)
		}
		if next == "" {
			break
		}
		cur = next
	}
	var swant []string
	for i := 1; i < 30; i += 4 {
		swant = append(swant, fmt.Sprintf("01JC00000000000000000001%02d", i))
	}
	if !slices.Equal(sids, swant) {
		t.Fatalf("session events = %v want %v", sids, swant)
	}

	// Summary: filtered counts and buckets.
	sumF := full
	sumF.AutoOnly, sumF.Limit = false, 0
	sum, err := s.AuditSummary(sumF)
	if err != nil {
		t.Fatal(err)
	}
	for k, n := range map[string]int64{analytics.KindAutoModeBlocked: 5, analytics.KindPolicyDenied: 2, analytics.KindPrivacyMasked: 4, analytics.KindOK: 4} {
		if sum.ByKind[k] != n {
			t.Fatalf("by_kind[%s] = %d want %d (%v)", k, sum.ByKind[k], n, sum.ByKind)
		}
	}
	if len(sum.Categories) != 1 || sum.Categories[0] != (analytics.AuditCount{Name: "email", Count: 15}) {
		t.Fatalf("categories = %+v", sum.Categories)
	}
	if len(sum.Denials) != 1 || sum.Denials[0].Code != "key_rate_limited" || sum.Denials[0].Count != 2 {
		t.Fatalf("denials = %+v", sum.Denials)
	}
	if a := sum.AutoMode; a.Block != 5 || a.Allow != 5 || a.Unknown != 5 || !slices.Equal(a.Signatures, []string{"claude-code-2.1"}) {
		t.Fatalf("auto = %+v", a)
	}
	allSum, err := s.AuditSummary(analytics.AuditFilter{SinceMs: since, UntilMs: until})
	if err != nil || len(allSum.Buckets) != 12 || allSum.CoverageStartMs != since {
		t.Fatalf("buckets=%d coverage=%d err=%v", len(allSum.Buckets), allSum.CoverageStartMs, err)
	}
	var total int64
	for i, b := range allSum.Buckets {
		total += b.Requests
		w := int64(0)
		if i%2 == 0 {
			w = 5
		}
		if b.Requests != w {
			t.Fatalf("bucket %d = %d want %d", i, b.Requests, w)
		}
	}
	if total != 30 {
		t.Fatalf("bucket total = %d", total)
	}

	// Rebuild clears and a re-ingest repopulates both console tables.
	if auditCount(t, s, "audit_event_categories") == 0 {
		t.Fatal("categories not projected")
	}
	if err := s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if auditCount(t, s, "audit_events") != 0 || auditCount(t, s, "audit_event_categories") != 0 {
		t.Fatal("rebuild must clear console tables")
	}
	epoch = acquire(t, s)
	if err := s.ingestBatch(ctx, "holder", epoch, auditSeed(base), map[string]int64{}); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if auditCount(t, s, "audit_events") != 30 || auditCount(t, s, "audit_event_categories") != 30 {
		t.Fatalf("rebuild repopulate: events=%d categories=%d", auditCount(t, s, "audit_events"), auditCount(t, s, "audit_event_categories"))
	}
}

func TestPgstoreAuditEventDetailHashLinksChain(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	fs, err := audit.NewFileSink(path, true)
	if err != nil {
		t.Fatal(err)
	}
	w, err := audit.NewWriter("inst", filepath.Join(dir, "wal"), []audit.Sink{fs})
	if err != nil {
		t.Fatal(err)
	}
	w.Append(auditRec("01JC0000000000000000000201", "request_completed", 200, nil))
	w.Append(auditRec("01JC0000000000000000000202", "request_completed", 200, nil))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var recs []audit.Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d", len(recs))
	}
	epoch := acquire(t, s)
	if err := s.ingestBatch(ctx, "holder", epoch, recs, map[string]int64{}); err != nil {
		t.Fatal(err)
	}
	first, ok, err := s.AuditEvent("01JC0000000000000000000201")
	if err != nil || !ok {
		t.Fatalf("first ok=%v err=%v", ok, err)
	}
	if first.Hash == "" || first.Hash != recs[1].PrevHash {
		t.Fatalf("hash = %q, next prev_hash = %q", first.Hash, recs[1].PrevHash)
	}
}

func TestEnsureSchemaAddsAuditTablesToMigratedDatabase(t *testing.T) {
	s := newTestStore(t) // projection marker already recorded
	ctx := context.Background()
	if _, err := s.db.Exec(ctx, `DROP TABLE audit_event_categories; DROP TABLE audit_events`); err != nil {
		t.Fatal(err)
	}
	s2, err := New(ctx, testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if auditCount(t, s2, "audit_events") != 0 || auditCount(t, s2, "audit_event_categories") != 0 {
		t.Fatal("tables should exist and be empty")
	}
}
