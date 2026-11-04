package analytics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
)

func probeSession(i int) string { return "s1:" + fmt.Sprintf("%032d", i) }

// seedAuditRows writes 30 rows with timestamps tied in groups of five.
// Even i: team platform, provider anthropic. i%3==0 masked, i%3 selects the
// auto verdict (0 allow, 1 block, 2 unknown), i%7==0 is a 429 gateway denial.
func seedAuditRows(t *testing.T, ix *Index, base time.Time) {
	t.Helper()
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("01JC00000000000000000001%02d", i)
		ts := base.Add(time.Duration(i/5) * time.Hour).Format(time.RFC3339Nano)
		r := rec(id, "request_completed", 200, func(r *audit.Record) {
			r.TS = ts
			r.Principal.Team = []string{"platform", "data"}[i%2]
			r.Request.Provider = []string{"anthropic", "bedrock"}[i%2]
			r.Request.Routing = &audit.RoutingRef{RequestedModel: "sonnet", ActualModel: "glm", Masked: i%3 == 0, Categories: []string{"email"}}
			r.Client = &audit.ClientRef{RequestClass: "main", SessionRef: probeSession(i % 4)}
			r.TraceID = str("trace1")
			r.AutoMode = &audit.AutoModeRef{Verdict: []string{"allow", "block", "unknown"}[i%3], Stage: "s1", Signature: "claude-code-2.1"}
			if i%7 == 0 {
				r.Event = "request_started"
				r.Outcome.Status = 429
				r.Outcome.Error = str("key_rate_limited")
			}
		})
		if err := ix.IngestAudit(r, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditAllFiltersPaginateWithoutDuplicates(t *testing.T) {
	ix := openIndex(t)
	base := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	seedAuditRows(t, ix, base)
	full := AuditFilter{Kinds: []string{KindOK, KindPrivacyMasked, KindAutoModeBlocked, KindPolicyDenied}, Team: "platform", KeyID: "ik_a", Model: "sonnet",
		Provider: "anthropic", Category: "email", Class: "main", TraceID: "trace1", PII: true, AutoOnly: true,
		SinceMs: base.UnixMilli(), UntilMs: base.Add(6 * time.Hour).UnixMilli(), Limit: 3}

	var ids []string
	cur := ""
	for page := 0; page < 20; page++ {
		f := full
		f.Before = cur
		got, next, err := ix.AuditEvents(f)
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
	for i := 28; i >= 0; i -= 2 { // even rows, newest id first
		if i%3 == 0 {
			continue // auto=true lists block/unknown verdicts only; rows with i%3==0 are allow
		}
		want = append(want, fmt.Sprintf("01JC00000000000000000001%02d", i))
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("paged ids = %v\nwant %v", ids, want)
	}
}

func TestAuditCodeSessionAndDefaultLimit(t *testing.T) {
	ix := openIndex(t)
	seedAuditRows(t, ix, time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	var ids []string
	cur := ""
	for {
		got, next, err := ix.AuditEvents(AuditFilter{Code: "key_rate_limited", Session: probeSession(0), Limit: 1, Before: cur})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range got {
			ids = append(ids, e.ID)
		}
		if next == "" {
			break
		}
		cur = next
	}
	if !slices.Equal(ids, []string{"01JC0000000000000000000128", "01JC0000000000000000000100"}) {
		t.Fatalf("code+session ids = %v", ids)
	}
	all, next, err := ix.AuditEvents(AuditFilter{})
	if err != nil || len(all) != 30 || next != "" {
		t.Fatalf("zero limit = %d rows next=%q err=%v", len(all), next, err)
	}
}

func TestAuditSessionsBeforeCursorWithHaving(t *testing.T) {
	ix := openIndex(t)
	base := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	seedAuditRows(t, ix, base)
	f := AuditFilter{Team: "platform", Model: "sonnet", Category: "email", PII: true, SinceMs: base.UnixMilli(), UntilMs: base.Add(6 * time.Hour).UnixMilli(), Limit: 1}
	var refs []string
	cur := ""
	for page := 0; page < 10; page++ {
		g := f
		g.Before = cur
		got, next, err := ix.AuditSessions(g)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range got {
			refs = append(refs, s.SessionRef)
		}
		if next == "" {
			break
		}
		cur = next
	}
	// Even rows fall in sessions 0 and 2; session 2 holds the newest id (i=26 vs 28 -> 28 is session 0).
	if !slices.Equal(refs, []string{probeSession(0), probeSession(2)}) {
		t.Fatalf("sessions = %v", refs)
	}
}

func TestAuditSessionEventsAfterCursor(t *testing.T) {
	ix := openIndex(t)
	seedAuditRows(t, ix, time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	var ids []string
	cur := ""
	for {
		got, next, err := ix.AuditSessionEvents(probeSession(1), AuditFilter{Limit: 2, After: cur})
		if err != nil {
			t.Fatal(err)
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
	for i := 1; i < 30; i += 4 {
		want = append(want, fmt.Sprintf("01JC00000000000000000001%02d", i))
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("session events = %v want %v", ids, want)
	}
}

func TestAuditSummaryFiltersAndBuckets(t *testing.T) {
	ix := openIndex(t)
	base := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	seedAuditRows(t, ix, base)
	since, until := base.UnixMilli(), base.Add(6*time.Hour).UnixMilli()
	full := AuditFilter{Kinds: []string{KindOK, KindPrivacyMasked, KindAutoModeBlocked, KindPolicyDenied}, Team: "platform", KeyID: "ik_a", Model: "sonnet",
		Provider: "anthropic", Category: "email", Class: "main", TraceID: "trace1", PII: true, SinceMs: since, UntilMs: until} // no AutoOnly: it would drop the allow rows these counts include
	sum, err := ix.AuditSummary(full)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]int64{KindAutoModeBlocked: 5, KindPolicyDenied: 2, KindPrivacyMasked: 4, KindOK: 4}
	for k, n := range wantKinds {
		if sum.ByKind[k] != n {
			t.Fatalf("by_kind[%s] = %d, want %d (%v)", k, sum.ByKind[k], n, sum.ByKind)
		}
	}
	if len(sum.Categories) != 1 || sum.Categories[0] != (AuditCount{Name: "email", Count: 15}) {
		t.Fatalf("categories = %+v", sum.Categories)
	}
	if len(sum.Denials) != 1 || sum.Denials[0].Code != "key_rate_limited" || sum.Denials[0].Count != 2 {
		t.Fatalf("denials = %+v", sum.Denials)
	}
	if a := sum.AutoMode; a.Block != 5 || a.Allow != 5 || a.Unknown != 5 || !slices.Equal(a.Signatures, []string{"claude-code-2.1"}) {
		t.Fatalf("auto = %+v", a)
	}

	all, err := ix.AuditSummary(AuditFilter{SinceMs: since, UntilMs: until})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Buckets) != 12 {
		t.Fatalf("buckets = %d", len(all.Buckets))
	}
	var total int64
	for i, b := range all.Buckets {
		total += b.Requests
		want := int64(0)
		if i%2 == 0 && i < 12 {
			want = 5 // each hour holds five rows and spans two half-hour buckets; rows sit at the hour start
		}
		if b.Requests != want {
			t.Fatalf("bucket %d requests = %d, want %d", i, b.Requests, want)
		}
	}
	if total != 30 {
		t.Fatalf("bucket total = %d", total)
	}
	sinceOnly, err := ix.AuditSummary(AuditFilter{SinceMs: since})
	if err != nil || len(sinceOnly.Buckets) != 0 {
		t.Fatalf("since-only buckets = %d err=%v", len(sinceOnly.Buckets), err)
	}
}

func TestAuditSummaryTieOrderingIsDeterministic(t *testing.T) {
	ix := openIndex(t)
	put := func(id, code, sig string) {
		r := rec(id, "request_started", 403, func(r *audit.Record) {
			r.Outcome.Error = str(code)
			r.AutoMode = &audit.AutoModeRef{Verdict: "allow", Stage: "s1", Signature: sig}
		})
		if err := ix.IngestAudit(r, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Tied counts and identical timestamps; insertion order is the reverse of the expected order.
	put("01JC0000000000000000000031", "model_forbidden", "z-sig")
	put("01JC0000000000000000000032", "key_rate_limited", "a-sig")
	for range 5 {
		sum, err := ix.AuditSummary(AuditFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(sum.Denials) != 2 || sum.Denials[0].Code != "key_rate_limited" || sum.Denials[1].Code != "model_forbidden" {
			t.Fatalf("denials = %+v", sum.Denials)
		}
		if !slices.Equal(sum.AutoMode.Signatures, []string{"a-sig", "z-sig"}) {
			t.Fatalf("signatures = %v", sum.AutoMode.Signatures)
		}
	}
}

func TestSinkLiveWritesAuditEvents(t *testing.T) {
	ix := openIndex(t)
	s := NewSink(ix)
	completed := `{"schema_version":1,"event":"request_completed","id":"01JC0000000000000000000001","ts":"2026-10-29T01:00:00Z","instance":"i","principal":{"key_id":"ik_a","team":"platform"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"outcome":{"status":200,"fallback_used":false,"partial":false,"error":null},"cost":{"amount_usd_micros":5},"trace_id":null,"prev_hash":""}`
	denied := `{"schema_version":1,"event":"request_started","id":"01JC0000000000000000000002","ts":"2026-10-29T01:00:01Z","instance":"i","principal":{"key_id":"ik_a","team":"platform"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"outcome":{"status":429,"fallback_used":false,"partial":false,"error":"key_rate_limited"},"trace_id":null,"prev_hash":""}`
	admitted := `{"schema_version":1,"event":"request_started","id":"01JC0000000000000000000003","ts":"2026-10-29T01:00:02Z","instance":"i","principal":{"key_id":"ik_a","team":"platform"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"trace_id":null,"prev_hash":""}`
	for _, l := range []string{completed, denied, admitted} {
		buf := []byte(l + "\n")
		_ = s.Write(buf)
		for i := range buf { // the audit writer reuses its buffer; the sink must have copied
			buf[i] = 'x'
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got, _, err := ix.AuditEvents(AuditFilter{Limit: 10})
	if err != nil || len(got) != 2 {
		t.Fatalf("audit rows = %d err=%v", len(got), err)
	}
	d, ok, err := ix.AuditEvent("01JC0000000000000000000002")
	if err != nil || !ok || string(d.Record) != denied {
		t.Fatalf("record not verbatim: ok=%v err=%v got=%s", ok, err, d.Record)
	}
	var n int
	if err := ix.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n); err != nil || n != 1 {
		t.Fatalf("events rows = %d err=%v (usage projection must keep only completed)", n, err)
	}
}

var pgPlaceholder = regexp.MustCompile(`\$(\d+)`)

func checkPostgresQuery(t *testing.T, name, q string, args []any) {
	t.Helper()
	seen := map[int]bool{}
	maxN := 0
	for _, m := range pgPlaceholder.FindAllStringSubmatch(q, -1) {
		n, _ := strconv.Atoi(m[1])
		seen[n] = true
		maxN = max(maxN, n)
	}
	if maxN != len(args) || len(seen) != len(args) {
		t.Errorf("%s: placeholders max=%d distinct=%d args=%d\n%s", name, maxN, len(seen), len(args), q)
	}
	if strings.Contains(q, "?") {
		t.Errorf("%s: stray ? in postgres query", name)
	}
}

func TestPostgresDialectPlaceholdersMatchArgs(t *testing.T) {
	full := AuditFilter{Kinds: []string{KindOK, KindPolicyDenied}, Team: "t", KeyID: "k", Model: "m", Provider: "p", Code: "c",
		Category: "email", Class: "main", Session: "s", TraceID: "tr", PII: true, AutoOnly: true, SinceMs: 1, UntilMs: 100000, Limit: 5, Before: "b", After: "a"}
	run := func(name string) QueryFunc {
		return func(q string, args []any, each func(RowScanner) error) error {
			checkPostgresQuery(t, name, q, args)
			return nil
		}
	}
	if _, _, err := RunAuditEvents(PostgresDialect, run("events"), full); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunAuditSessions(PostgresDialect, run("sessions"), full); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunAuditSessionEvents(PostgresDialect, run("sessionEvents"), "ref", full); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunAuditEvent(PostgresDialect, run("event"), "id"); err != nil {
		t.Fatal(err)
	}
	if _, err := RunAuditSummary(PostgresDialect, run("summary"), full); err != nil {
		t.Fatal(err)
	}
	execs := 0
	exec := func(q string, args ...any) error { execs++; checkPostgresQuery(t, "insert", q, args); return nil }
	row, _ := ProjectAuditEvent(rec("01X", "request_completed", 200, nil), nil)
	row.Categories = []string{"email", "phone"}
	if err := InsertAuditEvent(PostgresDialect, exec, row); err != nil {
		t.Fatal(err)
	}
	if execs != 3 {
		t.Fatalf("insert statements = %d, want 1 row + 2 categories", execs)
	}
}

// auto=true lists only block and unparsed verdicts; allow verdicts are
// counted in the summary but never listed.
func TestAuditAutoOnlyExcludesAllow(t *testing.T) {
	ix := openIndex(t)
	seedAuditRows(t, ix, time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	got, _, err := ix.AuditEvents(AuditFilter{AutoOnly: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range got {
		seen[e.AutoVerdict]++
	}
	if seen["allow"] != 0 || seen["block"] != 10 || seen["unknown"] != 10 || len(got) != 20 {
		t.Fatalf("auto-only verdicts = %v (rows %d)", seen, len(got))
	}
}

// The detail hash is the chain hash of the stored line, so it equals the next
// record's prev_hash. It is chain position, not a verification result.
func TestAuditEventDetailHashLinksChain(t *testing.T) {
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
	w.Append(rec("01JC0000000000000000000201", "request_completed", 200, nil))
	w.Append(rec("01JC0000000000000000000202", "request_completed", 200, nil))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ix := openIndex(t)
	if _, err := ix.Replay(f); err != nil {
		t.Fatal(err)
	}
	first, ok, err := ix.AuditEvent("01JC0000000000000000000201")
	if err != nil || !ok {
		t.Fatalf("first: %v %v", ok, err)
	}
	second, ok, err := ix.AuditEvent("01JC0000000000000000000202")
	if err != nil || !ok {
		t.Fatalf("second: %v %v", ok, err)
	}
	var next audit.Record
	if err := json.Unmarshal(second.Record, &next); err != nil {
		t.Fatal(err)
	}
	if first.Hash == "" || first.Hash != next.PrevHash {
		t.Fatalf("hash = %q, next prev_hash = %q", first.Hash, next.PrevHash)
	}
}
