# Audit Console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give full administrators an Audit view in the mayu console (Traces, Sessions, PII, Denials) built only from tamper-evident audit records, plus the append-only audit evidence it needs.

**Architecture:** A request-scoped evidence collector rides in the request context from a data-plane middleware (client class, key-scoped session digest, start time). Every ingress fallback loop records attempts into it, and every terminal audit record is stamped by one call, `audit.Finish`. The analytics sink and boot replay project terminal records into a new `audit_events` table; the SQL builder is shared by SQLite (Mode A) and Postgres (Mode B). A read-only `/admin/audit/*` API serves the new `static/audit.js` view.

**Tech Stack:** Go 1.26.10, `modernc.org/sqlite`, `pgx/v5`, dependency-free browser JS, Node (standard library only) and Python Playwright for console checks.

**Spec:** `docs/superpowers/specs/2026-10-29-audit-console-design.md` (including its "Amendments during planning" section) and `docs/decisions/ADR-057-audit-console-evidence.md`.

## Global Constraints

- Worktree: `/home/atomoh/.tmp/inferplane-audit-console-20261029/worktree`, branch `feat/audit-console` (no upstream). Never push or merge without the user's explicit approval.
- Every commit: `git commit -s` and the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- New audit fields are appended after `GuardrailVersion`, `omitempty`; a record without them must marshal to the same bytes as before.
- `request_class` keeps only `main`, `subagent`, `workflow`, `compaction`, `auxiliary`, `side_reply`.
- `session_ref = "s1:" + hex(sha256("inferplane-session-v1\x00" + key_id + "\x00" + session_id)[:16])`; session IDs are accepted only as 1–128 bytes of `[A-Za-z0-9._:-]`. The raw ID never reaches audit, metrics, logs or API output.
- Auto mode: only `auxiliary` requests with Anthropic-format responses; read at most 16 KiB of text; `verdict` ∈ `block | allow | unknown`; `signature` = `claude-code-2.1`; one unlabelled counter `inferplane_audit_auto_mode_unparsed_total`.
- `attempts` holds at most 8 entries (first seven and the last); `target` is the configured provider name, never a URL.
- Kinds: `ok`, `policy_denied`, `privacy_blocked`, `privacy_masked`, `privacy_internal`, `upstream_error` (shown as “Error”), `auto_mode_blocked`.
- `/admin/audit/*` is full-admin only (`AdminAuth` + `requireAdmin`), read-only, mounted only when the analytics querier implements `analytics.AuditEventQuerier`.
- Console: textContent-only rendering (`app.js:6`), no browser storage (ADR-001), CSP unchanged, i18n keys in en/ko/zh/ja.
- Builds and caches stay out of `/tmp`; long command output goes to a log file that is then grepped, never piped to `head`/`tail`.
- Do not touch the running `mayu.service` or `/home/atomoh/inferplane-local/`; live checks use a separate test gateway on ports 18180–19191.
- Each task that changes a layer updates its reference doc and module `CLAUDE.md` in Task 13, not piecemeal; `AGENTS.md` is regenerated, never hand-edited.

## Review Focus

1. **Responses writes one `request_completed` per attempt** (`internal/server/responsesapi/attempt.go`, `finish`). A fallback request therefore shows two rows; the second row must list both attempts and the first row (status 503) must derive to kind `upstream_error`, never `ok`. Pinned by `TestResponsesFallbackRowsCarryCumulativeAttempts` (Task 6) and the completed-5xx case of `TestKindOf` (Task 8).
2. **Time filters with offsets.** `since=2026-10-29T09:00:00+09:00` must mean 00:00Z, not 09:00Z. Pinned by `TestParseFilterNormalizesOffsets` in Task 9.
3. **Ambiguous key names.** Two keys with the same `metadata.name` must not silently filter to one of them; the console asks for the `key_id`. Pinned by the `auditResolveKey ambiguous` case in Task 10.
4. **Legacy records in the detail view.** A record with no `latency` and no `attempts` must render "no timing recorded", not a zero-width bar or `NaN%`. Pinned by `TestWaterfallLegacyRecord` (Task 8) and the `auditBar zero total` Node case (Task 10).
5. **Boot replay cost on a real audit file.** Projecting every terminal record doubles replay work at startup. Expected: a copy of the real local audit file replays without error and in under 60 s. Measured in Task 14 Step 3.

---

### Task 1: Early verification (capture, throwaway)

Answers the spec's three early verification items before any product code. Nothing here is committed except the findings appended to the spec.

**Files:**
- Create (outside the repo): `/home/atomoh/.tmp/inferplane-audit-console-20261029/verify/{mkconfig.py,capture.py,analyze.py}`
- Modify: `docs/superpowers/specs/2026-10-29-audit-console-design.md` (append "Early verification results" in both languages)

**Interfaces:**
- Produces: findings that may change constants in Task 7 (`blockStop`, stage names) and the stream tap requirement. Record each as `Task 1: Ruling: …` in the ledger.

- [ ] **Step 1: Build the current gateway into the verify directory**

```bash
V=/home/atomoh/.tmp/inferplane-audit-console-20261029/verify; mkdir -p $V
cd /home/atomoh/.tmp/inferplane-audit-console-20261029/worktree
CGO_ENABLED=0 go build -trimpath -o $V/mayu ./cmd/mayu > $V/build.log 2>&1; echo "exit=$?"
```
Expected: `exit=0`.

- [ ] **Step 2: Write the test config generator**

`$V/mkconfig.py` copies providers, models and pricing from the local config and drops everything else (no control plane, no identity binding, separate ports and stores):

```python
import json, pathlib, sys
V = pathlib.Path(sys.argv[1])
src = json.load(open('/home/atomoh/inferplane-local/config.json'))
cfg = {
    'server': {'listen': '127.0.0.1:18181', 'admin_listen': '127.0.0.1:19191',
               'admin_auth': {'token_refs': [{'env': 'INFERPLANE_ADMIN_TOKEN'}]}},
    'key_store': {'type': 'sqlite', 'path': str(V / 'keys.db')},
    'audit': {'failure_mode': 'buffer_then_block', 'buffer': {'path': str(V / 'audit.wal')},
              'sinks': [{'type': 'file', 'path': str(V / 'audit.jsonl')}]},
    'providers': src['providers'], 'models': src['models'], 'pricing': src['pricing'],
}
(V / 'config.json').write_text(json.dumps(cfg, indent=2))
print('models', len(cfg['models']))
```

Run: `python3 $V/mkconfig.py $V && INFERPLANE_ADMIN_TOKEN=lint $V/mayu config validate --config $V/config.json > $V/validate.log 2>&1; echo "exit=$?"`
Expected: `models 54` and `exit=0`.

- [ ] **Step 3: Create a test admin token and key**

```bash
umask 077; python3 -c 'import secrets;print(secrets.token_hex(24))' > $V/admin.txt
INFERPLANE_ADMIN_TOKEN=$(cat $V/admin.txt) $V/mayu keys create --team audit-test --models '*' --store $V/keys.db > $V/key.out 2>&1; echo "exit=$?"
grep -o 'ik_[A-Za-z0-9_-]*' $V/key.out | head -n1 > $V/key.txt; test -s $V/key.txt && echo key-saved
```
Expected: `exit=0`, `key-saved`. Never print `key.txt` or `admin.txt`.

- [ ] **Step 4: Write the capture proxy**

`$V/capture.py` relays `127.0.0.1:18180 → 127.0.0.1:18181`, streaming bodies through, and logs one JSON line per request to `$V/capture.jsonl`. It records header names and values for `x-claude-code-*` (the session ID only as a SHA-256 prefix), request shape (path, model, stream, max_tokens, stop_sequences) and, for `auxiliary` requests only, the first 2 KiB of response text.

```python
import hashlib, http.client, json, re, sys, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
OUT = open(sys.argv[1], 'a', buffering=1); LOCK = threading.Lock()
class H(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def log_message(self, *a): pass
    def do_POST(self): self.relay()
    def do_GET(self): self.relay()
    def relay(self):
        body = self.rfile.read(int(self.headers.get('Content-Length') or 0))
        hdr = {k.lower(): v for k, v in self.headers.items()}
        rec = {'path': self.path, 'method': self.command,
               'cc': {k: v for k, v in hdr.items() if k.startswith('x-claude-code-') and k != 'x-claude-code-session-id'}}
        if 'x-claude-code-session-id' in hdr:
            rec['session_sha'] = hashlib.sha256(hdr['x-claude-code-session-id'].encode()).hexdigest()[:12]
            rec['session_len'] = len(hdr['x-claude-code-session-id'])
        try:
            j = json.loads(body or b'{}')
            rec.update({k: j.get(k) for k in ('model', 'stream', 'max_tokens', 'stop_sequences')})
        except ValueError:
            rec['body'] = 'non-json'
        up = http.client.HTTPConnection('127.0.0.1', 18181, timeout=600)
        fwd = {k: v for k, v in self.headers.items() if k.lower() not in ('host', 'content-length', 'connection')}
        fwd['Content-Length'] = str(len(body))
        up.request(self.command, self.path, body=body, headers=fwd)
        resp = up.getresponse()
        self.send_response(resp.status)
        for k, v in resp.getheaders():
            if k.lower() not in ('transfer-encoding', 'connection', 'content-length'):
                self.send_header(k, v)
        self.send_header('Transfer-Encoding', 'chunked'); self.end_headers()
        keep = hdr.get('x-claude-code-request-class') == 'auxiliary'; sample = bytearray()
        while True:
            chunk = resp.read1(65536)
            if not chunk: break
            if keep and len(sample) < 2048: sample += chunk[:2048 - len(sample)]
            self.wfile.write(b'%x\r\n%s\r\n' % (len(chunk), chunk)); self.wfile.flush()
        self.wfile.write(b'0\r\n\r\n'); self.wfile.flush()
        rec['status'] = resp.status; rec['ctype'] = resp.getheader('Content-Type')
        if keep: rec['aux_sample'] = sample.decode('utf-8', 'replace')
        with LOCK: OUT.write(json.dumps(rec) + '\n')
ThreadingHTTPServer(('127.0.0.1', 18180), H).serve_forever()
```

- [ ] **Step 5: Start the test gateway and the capture proxy (background)**

```bash
INFERPLANE_ADMIN_TOKEN=$(cat $V/admin.txt) $V/mayu serve --config $V/config.json > $V/mayu.log 2>&1 &   # run_in_background
python3 $V/capture.py $V/capture.jsonl > $V/capture.log 2>&1 &                                         # run_in_background
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18181/healthz
```
Expected: `200`.

- [ ] **Step 6: Run Claude Code through the capture in three modes**

Each run asks for one shell command, which in auto mode needs a classifier decision. Cost is a few cents.

```bash
cd /home/atomoh/.tmp/inferplane-audit-console-20261029/verify
COMMON='--permission-mode auto -p'
PROMPT='Run this shell command and report its output: ls /home/atomoh/.tmp'
# a) Bedrock mode, Claude main model (server-side verdict expected)
env -u ANTHROPIC_BASE_URL -u ANTHROPIC_API_KEY CLAUDE_CODE_USE_BEDROCK=1 AWS_REGION=ap-northeast-2 \
  ANTHROPIC_BEDROCK_BASE_URL=http://127.0.0.1:18180 CLAUDE_CODE_SKIP_BEDROCK_AUTH=1 AWS_BEARER_TOKEN_BEDROCK="$(cat key.txt)" \
  ANTHROPIC_MODEL=global.anthropic.claude-opus-5-5 claude $COMMON "$PROMPT" > run-a.txt 2>&1; echo "a=$?"
# b) Bedrock mode, non-Claude main model (local classifier expected)
env -u ANTHROPIC_BASE_URL -u ANTHROPIC_API_KEY CLAUDE_CODE_USE_BEDROCK=1 AWS_REGION=ap-northeast-2 \
  ANTHROPIC_BEDROCK_BASE_URL=http://127.0.0.1:18180 CLAUDE_CODE_SKIP_BEDROCK_AUTH=1 AWS_BEARER_TOKEN_BEDROCK="$(cat key.txt)" \
  ANTHROPIC_MODEL=kimi-k3 claude $COMMON "$PROMPT" > run-b.txt 2>&1; echo "b=$?"
# c) Anthropic mode
env -u CLAUDE_CODE_USE_BEDROCK -u ANTHROPIC_BEDROCK_BASE_URL ANTHROPIC_BASE_URL=http://127.0.0.1:18180 ANTHROPIC_API_KEY="$(cat key.txt)" \
  ANTHROPIC_MODEL=global.anthropic.claude-sonnet-5 claude $COMMON "$PROMPT" > run-c.txt 2>&1; echo "c=$?"
```
Expected: three exit codes recorded (a non-zero exit is a finding, not a failure of this task).

- [ ] **Step 7: Summarize the capture**

`$V/analyze.py`:

```python
import collections, json, re, sys
rows = [json.loads(l) for l in open(sys.argv[1])]
print('requests', len(rows))
print('paths', collections.Counter(r['path'].split('?')[0].rsplit('/', 1)[-1] if '/model/' in r['path'] else r['path'] for r in rows))
print('class', collections.Counter(r['cc'].get('x-claude-code-request-class') for r in rows))
print('session header present', sum('session_sha' in r for r in rows), 'distinct', len({r.get('session_sha') for r in rows if 'session_sha' in r}), 'max_len', max([r.get('session_len', 0) for r in rows] or [0]))
for r in rows:
    if r['cc'].get('x-claude-code-request-class') == 'auxiliary':
        s = r.get('aux_sample', '')
        print('AUX', r['path'][-40:], 'stream=', r.get('stream'), 'stops=', r.get('stop_sequences'), 'max=', r.get('max_tokens'),
              'block=', re.findall(r'<block>\s*\w+', s)[:3], 'thinking=', '<thinking>' in s, 'ctype=', r.get('ctype'))
```

Run: `python3 $V/analyze.py $V/capture.jsonl > $V/findings.txt; cat $V/findings.txt`
Expected: a readable summary. Answer from it, per mode (a/b/c):
1. Are `X-Claude-Code-Session-Id` and `x-claude-code-request-class` present?
2. For classifier calls: streaming or not, stop sequences, where `<block>` appears, stage distinction.
3. Does mode (a) show any classifier calls? If not, the server-side verdict is invisible to the gateway.

- [ ] **Step 8: Record findings and rulings**

Append to the spec, before "## Out of scope" and before "## 범위 밖", a section "## Early verification results (2026-10-29)" / "## 먼저 확인한 결과 (2026-10-29)" with the answers above, one line each, plus these rulings when they apply (write each also to the ledger as `Task 1: Ruling: …`):
- A mode without the headers: that mode has no Sessions/class data; the console states it (no code change).
- Classifier stop sequence is not `</block>`: change `blockStop` in Task 7 before writing its tests.
- Classifier calls stream: keep the stream tap in Task 7 (already planned).
- A stage marker other than the stop sequence: add it to Task 7's stage rule and its table test.

Run: `python3 scripts/check_docs_translations.py > /dev/null; echo "exit=$?"` → `exit=0`.

- [ ] **Step 9: Stop the throwaway processes and commit the findings**

```bash
pkill -f "$V/mayu serve" ; pkill -f "$V/capture.py"
git add docs/superpowers/specs/2026-10-29-audit-console-design.md
git commit -s -m "docs: record audit console early verification results

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Keep `$V` until Task 14 (fixtures in Task 7 are derived from it); Task 14 removes the response samples.

---

### Task 2: Audit evidence fields and request evidence collector

**Files:**
- Modify: `internal/audit/record.go` (three fields at the end of `Record`)
- Modify: `internal/audit/deny_reason.go` (add `KnownDenyReason`)
- Create: `internal/audit/evidence.go`
- Test: `internal/audit/evidence_test.go`

**Interfaces:**
- Produces:
  - `type AttemptRef struct{ Provider, Model, Status string; HTTP int; StartMs, DurMs int64 }`
  - `type ClientRef struct{ RequestClass, SessionRef string }`
  - `type AutoModeRef struct{ Verdict, Stage, Signature string }`
  - `Record.Attempts []AttemptRef`, `Record.Client *ClientRef`, `Record.AutoMode *AutoModeRef`
  - constants `AttemptOK`, `AttemptUpstreamError`, `AttemptModelNotFound`, `AttemptStreamFailed`, `AttemptTimeout`, `AttemptCanceled`
  - `func WithEvidence(ctx context.Context, start time.Time, client *ClientRef) context.Context`
  - `func ClientFrom(ctx context.Context) *ClientRef`
  - `func BeginAttempt(ctx context.Context, provider, model string) func(status string, httpStatus int)`
  - `func NoteAttemptHTTP(ctx context.Context, httpStatus int)`
  - `func MarkFirstToken(ctx context.Context)`
  - `func SetAutoMode(ctx context.Context, ref *AutoModeRef)`
  - `func Finish(ctx context.Context, rec *Record)`
  - `func KnownDenyReason(code string) bool`

- [ ] **Step 1: Write the failing tests**

`internal/audit/evidence_test.go`:

```go
package audit

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A record without the new fields must keep its historical bytes.
func TestRecordWithoutEvidenceKeepsBytes(t *testing.T) {
	r := Record{SchemaVersion: 1, Event: "request_completed", ID: "01J", TS: "2026-10-29T00:00:00Z", Instance: "i",
		Principal: PrincipalRef{KeyID: "ik", Team: "t"}, Request: RequestRef{Ingress: "anthropic", ModelRequested: "m"},
		Outcome: &OutcomeRef{Status: 200}}
	got, err := r.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"event":"request_completed","id":"01J","ts":"2026-10-29T00:00:00Z","instance":"i","principal":{"key_id":"ik","team":"t"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"outcome":{"status":200,"fallback_used":false,"partial":false,"error":null},"trace_id":null,"prev_hash":""}`
	if string(got) != want {
		t.Fatalf("legacy bytes changed:\n got %s\nwant %s", got, want)
	}
}

func TestFinishStampsCompletedRecord(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), &ClientRef{RequestClass: "main", SessionRef: "s1:00"})
	endA := BeginAttempt(ctx, "bad", "m1")
	NoteAttemptHTTP(ctx, 503)
	endA(AttemptUpstreamError, 0)
	BeginAttempt(ctx, "good", "m2") // left open: Finish closes it from the outcome
	MarkFirstToken(ctx)
	rec := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}}
	Finish(ctx, &rec)
	if len(rec.Attempts) != 2 {
		t.Fatalf("attempts = %+v", rec.Attempts)
	}
	if a := rec.Attempts[0]; a.Provider != "bad" || a.Status != AttemptUpstreamError || a.HTTP != 503 {
		t.Fatalf("first attempt = %+v", a)
	}
	if a := rec.Attempts[1]; a.Provider != "good" || a.Status != AttemptOK || a.HTTP != 200 {
		t.Fatalf("second attempt = %+v", a)
	}
	if !rec.Outcome.FallbackUsed || strings.Join(rec.Outcome.FallbackChain, ",") != "bad,good" {
		t.Fatalf("fallback fields = %+v", rec.Outcome)
	}
	if rec.Latency == nil || rec.Latency.TTFTMs < 1 {
		t.Fatalf("latency = %+v", rec.Latency)
	}
	if rec.Client == nil || rec.Client.RequestClass != "main" {
		t.Fatalf("client = %+v", rec.Client)
	}
}

func TestFinishDeniedStartedRecord(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), &ClientRef{RequestClass: "subagent"})
	rec := Record{Event: "request_started", Outcome: &OutcomeRef{Status: 403}}
	Finish(ctx, &rec)
	if rec.Client == nil || rec.Latency == nil || rec.Attempts != nil || rec.AutoMode != nil {
		t.Fatalf("denied record = %+v", rec)
	}
}

func TestFinishLeavesAdmittedStartedRecord(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), &ClientRef{RequestClass: "main"})
	rec := Record{Event: "request_started"}
	Finish(ctx, &rec)
	if rec.Client != nil || rec.Latency != nil {
		t.Fatalf("admitted started record must stay untouched: %+v", rec)
	}
}

func TestFinishKeepsExistingLatency(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	rec := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}, Latency: &LatencyRef{TotalMs: 42}}
	Finish(ctx, &rec)
	if rec.Latency.TotalMs != 42 {
		t.Fatalf("latency overwritten: %+v", rec.Latency)
	}
}

func TestAttemptsAreBounded(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	for i := range 10 {
		end := BeginAttempt(ctx, "p"+string(rune('a'+i)), "m")
		if i < 9 {
			end(AttemptUpstreamError, 503)
		}
	}
	rec := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}}
	Finish(ctx, &rec)
	if len(rec.Attempts) != 8 || rec.Attempts[6].Provider != "pg" || rec.Attempts[7].Provider != "pj" {
		t.Fatalf("bounded attempts = %+v", rec.Attempts)
	}
}

func TestAutoModeOnlyOnCompleted(t *testing.T) {
	ctx := WithEvidence(context.Background(), time.Now(), nil)
	SetAutoMode(ctx, &AutoModeRef{Verdict: "block", Stage: "s1", Signature: "claude-code-2.1"})
	denied := Record{Event: "request_started", Outcome: &OutcomeRef{Status: 429}}
	Finish(ctx, &denied)
	done := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}}
	Finish(ctx, &done)
	if denied.AutoMode != nil || done.AutoMode == nil || done.AutoMode.Verdict != "block" {
		t.Fatalf("auto mode: denied=%+v done=%+v", denied.AutoMode, done.AutoMode)
	}
}

func TestNoEvidenceIsNoop(t *testing.T) {
	end := BeginAttempt(context.Background(), "p", "m")
	end(AttemptOK, 200)
	MarkFirstToken(context.Background())
	rec := Record{Event: "request_completed", Outcome: &OutcomeRef{Status: 200}}
	Finish(context.Background(), &rec)
	if rec.Latency != nil || rec.Attempts != nil {
		t.Fatalf("no collector must mean no change: %+v", rec)
	}
}

func TestMixedChainVerifies(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []Sink{NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	w.Append(Record{SchemaVersion: 1, Event: "request_completed", ID: "01A", TS: "2026-10-29T00:00:00Z", Outcome: &OutcomeRef{Status: 200}})
	ctx := WithEvidence(context.Background(), time.Now(), &ClientRef{RequestClass: "main"})
	rec := Record{SchemaVersion: 1, Event: "request_completed", ID: "01B", TS: "2026-10-29T00:00:01Z", Outcome: &OutcomeRef{Status: 200}}
	Finish(ctx, &rec)
	w.Append(rec)
	w.Close()
	res, err := Verify(bytes.NewReader(buf.Bytes()))
	if err != nil || !res.OK || res.Records != 2 {
		t.Fatalf("verify = %+v, %v", res, err)
	}
}

func TestKnownDenyReason(t *testing.T) {
	if !KnownDenyReason("team_budget_exceeded") || !KnownDenyReason("pricing_missing") || KnownDenyReason("upstream_error") {
		t.Fatal("deny reason set wrong")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/audit/ -run 'Evidence|Finish|Attempts|AutoModeOnly|NoEvidence|MixedChain|KnownDeny' > /home/atomoh/.tmp/inferplane-audit-console-20261029/t2.log 2>&1; grep -E '^(ok|FAIL|---|.*undefined)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t2.log`
Expected: FAIL with `undefined: WithEvidence` (and the other new names).

- [ ] **Step 3: Add the record fields**

In `internal/audit/record.go`, inside `Record` after `GuardrailVersion`:

```go
	// ADR-057 audit console evidence, appended at the END with omitempty so
	// earlier records keep their exact bytes. Attempts are provider names and
	// closed statuses only; Client holds a closed request class and a
	// key-scoped session digest, never the raw session ID; AutoMode is an
	// estimate read from a Claude Code classifier response.
	Attempts []AttemptRef `json:"attempts,omitempty"`
	Client   *ClientRef   `json:"client,omitempty"`
	AutoMode *AutoModeRef `json:"auto_mode,omitempty"`
```

In `internal/audit/deny_reason.go`, after `Ptr`:

```go
// KnownDenyReason reports whether code is one of the closed deny codes above.
func KnownDenyReason(code string) bool {
	switch DenyReason(code) {
	case DenyModelNotAllowed, DenyTeamRateLimited, DenyTeamTokenRateLimited, DenyTeamQuotaExceeded,
		DenyKeyRateLimited, DenyKeyTokenRateLimited, DenyTeamBudgetExceeded, DenyKeyBudgetExceeded,
		DenyUserBudgetExceeded, DenyRegionBlocked, DenyPricingMissing, DenyInvalidTokenEstimate:
		return true
	}
	return false
}
```

- [ ] **Step 4: Write the collector**

`internal/audit/evidence.go`:

```go
package audit

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Attempt statuses are a closed set (ADR-057).
const (
	AttemptOK            = "ok"
	AttemptUpstreamError = "upstream_error"
	AttemptModelNotFound = "model_not_found"
	AttemptStreamFailed  = "stream_failed"
	AttemptTimeout       = "timeout"
	AttemptCanceled      = "canceled"
)

// maxAttempts bounds attempts[] on one record: the first seven and the last.
const maxAttempts = 8

// AttemptRef is one provider call: the configured provider name (never a
// URL), the model, a closed status, the upstream HTTP status when known, and
// offsets from the request start.
type AttemptRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Status   string `json:"status"`
	HTTP     int    `json:"http,omitempty"`
	StartMs  int64  `json:"start_ms"`
	DurMs    int64  `json:"dur_ms"`
}

// ClientRef is bounded client context: an allow-listed request class and a
// key-scoped session digest.
type ClientRef struct {
	RequestClass string `json:"request_class,omitempty"`
	SessionRef   string `json:"session_ref,omitempty"`
}

// AutoModeRef is an estimated Claude Code auto mode classifier verdict.
type AutoModeRef struct {
	Verdict   string `json:"verdict"`
	Stage     string `json:"stage,omitempty"`
	Signature string `json:"signature"`
}

type evidence struct {
	mu       sync.Mutex
	start    time.Time
	client   *ClientRef
	ttft     time.Duration
	attempts []AttemptRef
	open     int
	auto     *AutoModeRef
}

type evidenceKey struct{}

// WithEvidence attaches one request's evidence collector. The data-plane
// middleware calls it once per request after authentication.
func WithEvidence(ctx context.Context, start time.Time, client *ClientRef) context.Context {
	e := &evidence{start: start, open: -1}
	if client != nil {
		c := *client
		e.client = &c
	}
	return context.WithValue(ctx, evidenceKey{}, e)
}

func evidenceFrom(ctx context.Context) *evidence {
	e, _ := ctx.Value(evidenceKey{}).(*evidence)
	return e
}

// ClientFrom returns a copy of the request's client evidence, or nil.
func ClientFrom(ctx context.Context) *ClientRef {
	e := evidenceFrom(ctx)
	if e == nil || e.client == nil {
		return nil
	}
	c := *e.client
	return &c
}

func (e *evidence) sinceMs() int64 { return time.Since(e.start).Milliseconds() }

// BeginAttempt records the start of one provider call and returns its end
// function. Ending twice is a no-op; an attempt still open when the terminal
// record is written is closed from that record's outcome.
func BeginAttempt(ctx context.Context, provider, model string) func(status string, httpStatus int) {
	e := evidenceFrom(ctx)
	if e == nil {
		return func(string, int) {}
	}
	e.mu.Lock()
	idx := len(e.attempts)
	e.attempts = append(e.attempts, AttemptRef{Provider: provider, Model: model, StartMs: e.sinceMs()})
	e.open = idx
	e.mu.Unlock()
	return func(status string, httpStatus int) {
		e.mu.Lock()
		e.endLocked(idx, status, httpStatus)
		e.mu.Unlock()
	}
}

func (e *evidence) endLocked(idx int, status string, httpStatus int) {
	a := &e.attempts[idx]
	if a.Status != "" {
		return
	}
	a.Status = status
	if httpStatus > 0 {
		a.HTTP = httpStatus
	}
	a.DurMs = max(e.sinceMs()-a.StartMs, 0)
	if e.open == idx {
		e.open = -1
	}
}

// NoteAttemptHTTP records the upstream status of the open attempt.
func NoteAttemptHTTP(ctx context.Context, httpStatus int) {
	e := evidenceFrom(ctx)
	if e == nil || httpStatus <= 0 {
		return
	}
	e.mu.Lock()
	if e.open >= 0 {
		e.attempts[e.open].HTTP = httpStatus
	}
	e.mu.Unlock()
}

// MarkFirstToken records time to first token once.
func MarkFirstToken(ctx context.Context) {
	e := evidenceFrom(ctx)
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.ttft == 0 {
		e.ttft = max(time.Since(e.start), time.Millisecond)
	}
	e.mu.Unlock()
}

// SetAutoMode stores the request's estimated classifier verdict.
func SetAutoMode(ctx context.Context, ref *AutoModeRef) {
	e := evidenceFrom(ctx)
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if ref == nil {
		e.auto = nil
		return
	}
	c := *ref
	e.auto = &c
}

// Finish stamps the collected evidence onto a terminal record: a
// request_completed record, or a request_started record that carries an
// outcome (how every denial is written). Every other record is unchanged.
func Finish(ctx context.Context, rec *Record) {
	e := evidenceFrom(ctx)
	if e == nil || rec == nil || !terminalRecord(rec) {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil {
		c := *e.client
		rec.Client = &c
	}
	if rec.Latency == nil {
		rec.Latency = &LatencyRef{TotalMs: e.sinceMs(), TTFTMs: e.ttft.Milliseconds()}
	}
	if e.open >= 0 {
		status, code := finalAttemptStatus(ctx, rec.Outcome)
		e.endLocked(e.open, status, code)
	}
	if len(e.attempts) > 0 {
		rec.Attempts = boundedAttempts(e.attempts)
	}
	if len(e.attempts) > 1 && rec.Outcome != nil {
		chain := make([]string, len(rec.Attempts))
		for i, a := range rec.Attempts {
			chain[i] = a.Provider
		}
		rec.Outcome.FallbackUsed = true
		rec.Outcome.FallbackChain = chain
	}
	if rec.Event == "request_completed" && e.auto != nil {
		a := *e.auto
		rec.AutoMode = &a
	}
}

func terminalRecord(r *Record) bool {
	return r.Event == "request_completed" || (r.Event == "request_started" && r.Outcome != nil)
}

func finalAttemptStatus(ctx context.Context, o *OutcomeRef) (string, int) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return AttemptCanceled, 0
	}
	if o == nil {
		return AttemptUpstreamError, 0
	}
	switch {
	case o.Partial:
		return AttemptStreamFailed, o.Status
	case o.Status >= 200 && o.Status < 400:
		return AttemptOK, o.Status
	case o.Status == 504:
		return AttemptTimeout, o.Status
	default:
		return AttemptUpstreamError, o.Status
	}
}

func boundedAttempts(in []AttemptRef) []AttemptRef {
	if len(in) <= maxAttempts {
		return slices.Clone(in)
	}
	out := slices.Clone(in[:maxAttempts-1])
	return append(out, in[len(in)-1])
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/audit/ -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t2.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t2.log`
Expected: `ok  github.com/inferplane/inferplane/internal/audit`.

- [ ] **Step 6: Commit**

```bash
git add internal/audit/record.go internal/audit/deny_reason.go internal/audit/evidence.go internal/audit/evidence_test.go
git commit -s -m "feat(audit): append attempt, client and auto mode evidence (ADR-057)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Client context, configuration and data-plane wiring

**Files:**
- Create: `internal/clientctx/clientctx.go`, `internal/clientctx/clientctx_test.go`
- Modify: `internal/config/config.go` (`AuditConfig.ClientEvidence`, `OTelConfig.TraceLinkTemplate`, validation)
- Test: `internal/config/audit_console_test.go`
- Modify: `internal/server/server.go` (`dataMuxOptions.clientEvidence`, `WithClientEvidence`, wrap `governed`)
- Test: `internal/server/client_evidence_test.go`
- Modify: `cmd/mayu/gateway.go` (pass `server.WithClientEvidence(clientEvidenceMode(cfg))`)

**Interfaces:**
- Consumes: `audit.WithEvidence`, `audit.ClientFrom`, `audit.ClientRef` (Task 2).
- Produces:
  - `type clientctx.Mode struct{ SessionRef, AutoMode bool }`, `func clientctx.DefaultMode() Mode`
  - `func clientctx.FromRequest(h http.Header, keyID string, m Mode) *audit.ClientRef`
  - `func clientctx.SessionRef(keyID, sessionID string) string`
  - `func clientctx.WithMode(ctx context.Context, m Mode) context.Context`, `func clientctx.ModeFrom(ctx context.Context) Mode`
  - `func clientctx.Middleware(m Mode, next http.Handler) http.Handler`
  - `func clientctx.AutoModeWanted(ctx context.Context) bool`
  - `const clientctx.ClassAuxiliary = "auxiliary"`
  - `config.ClientEvidenceConfig{SessionRef, AutoMode string}`; `config.OTelConfig.TraceLinkTemplate string`
  - `func server.WithClientEvidence(m clientctx.Mode) DataMuxOption`

- [ ] **Step 1: Write the failing clientctx tests**

`internal/clientctx/clientctx_test.go`:

```go
package clientctx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
)

func hdr(class, session string) http.Header {
	h := http.Header{}
	if class != "" {
		h.Set("x-claude-code-request-class", class)
	}
	if session != "" {
		h.Set("X-Claude-Code-Session-Id", session)
	}
	return h
}

func TestRequestClassAllowList(t *testing.T) {
	for class, want := range map[string]string{"main": "main", "auxiliary": "auxiliary", "side_reply": "side_reply", "AUXILIARY": "", "debug": "", "": ""} {
		ref := FromRequest(hdr(class, ""), "ik_1", DefaultMode())
		got := ""
		if ref != nil {
			got = ref.RequestClass
		}
		if got != want {
			t.Fatalf("class %q -> %q, want %q", class, got, want)
		}
	}
}

func TestSessionRefIsKeyScopedDigest(t *testing.T) {
	const id = "0b6f6a8e-2f7c-4c1e-9a77-raw-session"
	a := FromRequest(hdr("main", id), "ik_a", DefaultMode())
	b := FromRequest(hdr("main", id), "ik_b", DefaultMode())
	if a.SessionRef == b.SessionRef {
		t.Fatal("same session under two keys must yield two refs")
	}
	if !regexp.MustCompile(`^s1:[0-9a-f]{32}$`).MatchString(a.SessionRef) || strings.Contains(a.SessionRef, "raw-session") {
		t.Fatalf("bad ref %q", a.SessionRef)
	}
	if a.SessionRef != SessionRef("ik_a", id) {
		t.Fatal("FromRequest and SessionRef disagree")
	}
}

func TestInvalidSessionIDsAreDropped(t *testing.T) {
	for _, id := range []string{"has space", strings.Repeat("a", 129), "semi;colon", "ünïcode"} {
		if ref := FromRequest(hdr("", id), "ik", DefaultMode()); ref != nil {
			t.Fatalf("session %q accepted: %+v", id, ref)
		}
	}
}

func TestSessionRefOffKeepsClass(t *testing.T) {
	ref := FromRequest(hdr("subagent", "abc"), "ik", Mode{SessionRef: false, AutoMode: true})
	if ref == nil || ref.RequestClass != "subagent" || ref.SessionRef != "" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestMiddlewareAttachesEvidenceAndMode(t *testing.T) {
	var gotClient *audit.ClientRef
	var gotWanted bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClient = audit.ClientFrom(r.Context())
		gotWanted = AutoModeWanted(r.Context())
		rec := audit.Record{Event: "request_completed", Outcome: &audit.OutcomeRef{Status: 200}}
		audit.Finish(r.Context(), &rec)
		if rec.Latency == nil {
			t.Error("Finish found no collector")
		}
	})
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header = hdr("auxiliary", "sess-1")
	req = req.WithContext(principal.With(req.Context(), keystore.Principal{KeyID: "ik_m"}))
	Middleware(DefaultMode(), next).ServeHTTP(httptest.NewRecorder(), req)
	if gotClient == nil || gotClient.RequestClass != "auxiliary" || gotClient.SessionRef != SessionRef("ik_m", "sess-1") || !gotWanted {
		t.Fatalf("client=%+v wanted=%v", gotClient, gotWanted)
	}
}

func TestAutoModeWantedNeedsAuxiliaryAndMode(t *testing.T) {
	ctx := audit.WithEvidence(context.Background(), time.Now(), &audit.ClientRef{RequestClass: "main"})
	if AutoModeWanted(WithMode(ctx, DefaultMode())) {
		t.Fatal("main class must not be inspected")
	}
	aux := audit.WithEvidence(context.Background(), time.Now(), &audit.ClientRef{RequestClass: "auxiliary"})
	if AutoModeWanted(WithMode(aux, Mode{SessionRef: true})) {
		t.Fatal("auto_mode off must disable inspection")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/clientctx/ > /home/atomoh/.tmp/inferplane-audit-console-20261029/t3.log 2>&1; grep -E '^(ok|FAIL|---|.*(undefined|no Go files))' /home/atomoh/.tmp/inferplane-audit-console-20261029/t3.log`
Expected: FAIL (`no non-test Go files` or `undefined: FromRequest`).

- [ ] **Step 3: Implement clientctx**

`internal/clientctx/clientctx.go`:

```go
// Package clientctx turns bounded client request headers into audit evidence
// (ADR-057): an allow-listed Claude Code request class and a key-scoped,
// domain-separated session digest. The raw session ID never leaves this
// package. It also detects Claude Code auto mode classifier verdicts
// (automode.go).
package clientctx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/principal"
)

const (
	headerSession   = "X-Claude-Code-Session-Id"
	headerClass     = "X-Claude-Code-Request-Class"
	maxSessionBytes = 128

	// ClassAuxiliary is the class Claude Code uses for auto mode classifier calls.
	ClassAuxiliary = "auxiliary"
)

// requestClasses is the closed set Claude Code 2.1.312 sends.
var requestClasses = map[string]bool{
	"main": true, "subagent": true, "workflow": true,
	"compaction": true, ClassAuxiliary: true, "side_reply": true,
}

// Mode selects which client evidence the gateway records.
type Mode struct {
	SessionRef bool
	AutoMode   bool
}

// DefaultMode records both, as ADR-057 decides.
func DefaultMode() Mode { return Mode{SessionRef: true, AutoMode: true} }

// FromRequest returns the bounded client evidence for one request, or nil.
func FromRequest(h http.Header, keyID string, m Mode) *audit.ClientRef {
	ref := audit.ClientRef{}
	if c := h.Get(headerClass); requestClasses[c] {
		ref.RequestClass = c
	}
	if m.SessionRef {
		if id := h.Get(headerSession); validSessionID(id) {
			ref.SessionRef = SessionRef(keyID, id)
		}
	}
	if ref == (audit.ClientRef{}) {
		return nil
	}
	return &ref
}

// SessionRef is the key-scoped, domain-separated session digest.
func SessionRef(keyID, sessionID string) string {
	sum := sha256.Sum256([]byte("inferplane-session-v1\x00" + keyID + "\x00" + sessionID))
	return "s1:" + hex.EncodeToString(sum[:16])
}

func validSessionID(id string) bool {
	if len(id) == 0 || len(id) > maxSessionBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}

type modeKey struct{}

// WithMode records the configured evidence mode on the request context.
func WithMode(ctx context.Context, m Mode) context.Context { return context.WithValue(ctx, modeKey{}, m) }

// ModeFrom returns the configured mode; absent means everything off.
func ModeFrom(ctx context.Context) Mode {
	m, _ := ctx.Value(modeKey{}).(Mode)
	return m
}

// Middleware attaches request evidence. It runs inside KeyAuth so the key ID
// is known; the start time is taken before any handler work.
func Middleware(m Mode, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		p, _ := principal.From(r.Context())
		ctx := audit.WithEvidence(r.Context(), start, FromRequest(r.Header, p.KeyID, m))
		next.ServeHTTP(w, r.WithContext(WithMode(ctx, m)))
	})
}

// AutoModeWanted reports whether this request's response should be checked
// for an auto mode verdict.
func AutoModeWanted(ctx context.Context) bool {
	if !ModeFrom(ctx).AutoMode {
		return false
	}
	c := audit.ClientFrom(ctx)
	return c != nil && c.RequestClass == ClassAuxiliary
}
```

- [ ] **Step 4: Run clientctx tests**

Run: `go test ./internal/clientctx/ -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t3.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t3.log`
Expected: `ok`.

- [ ] **Step 5: Write the failing config tests**

`internal/config/audit_console_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadJSON(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestClientEvidenceConfig(t *testing.T) {
	cfg, err := loadJSON(t, `{"audit":{"client_evidence":{"session_ref":"off","auto_mode":"on"}}}`)
	if err != nil || cfg.Audit.ClientEvidence == nil || cfg.Audit.ClientEvidence.SessionRef != "off" {
		t.Fatalf("parse: %+v %v", cfg, err)
	}
	for _, bad := range []string{
		`{"audit":{"client_evidence":{"session_ref":"raw"}}}`,
		`{"audit":{"client_evidence":{"auto_mode":"maybe"}}}`,
	} {
		if _, err := loadJSON(t, bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestTraceLinkTemplate(t *testing.T) {
	if _, err := loadJSON(t, `{"otel":{"endpoint":"c:4318","trace_link_template":"https://grafana.example/explore?traceId={trace_id}"}}`); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	for _, bad := range []string{
		`{"otel":{"endpoint":"c","trace_link_template":"https://grafana.example/explore"}}`,
		`{"otel":{"endpoint":"c","trace_link_template":"javascript:alert({trace_id})"}}`,
		`{"otel":{"endpoint":"c","trace_link_template":"ftp://host/{trace_id}"}}`,
		`{"otel":{"endpoint":"c","trace_link_template":"https:///{trace_id}"}}`,
	} {
		if _, err := loadJSON(t, bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
```

Run: `go test ./internal/config/ -run 'ClientEvidence|TraceLink' > /home/atomoh/.tmp/inferplane-audit-console-20261029/t3c.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t3c.log`
Expected: FAIL (unknown field `client_evidence` or missing validation).

- [ ] **Step 6: Implement the config**

In `internal/config/config.go`:

```go
// ClientEvidenceConfig selects ADR-057 client evidence. Absent = both on.
type ClientEvidenceConfig struct {
	SessionRef string `json:"session_ref,omitempty"` // "key_scoped" (default) | "off"
	AutoMode   string `json:"auto_mode,omitempty"`   // "on" (default) | "off"
}
```

Add to `AuditConfig` after `LogBodies`: `ClientEvidence *ClientEvidenceConfig `json:"client_evidence,omitempty"``.
Add to `OTelConfig` after `ServiceName`: `TraceLinkTemplate string `json:"trace_link_template,omitempty"`` with the comment `// console link only; {trace_id} is replaced by a 32-hex trace ID (ADR-057)`.

Validation functions (place after `validateOTel`):

```go
func validateClientEvidence(c *ClientEvidenceConfig) error {
	if c == nil {
		return nil
	}
	switch c.SessionRef {
	case "", "key_scoped", "off":
	default:
		return fmt.Errorf("config: audit.client_evidence.session_ref must be \"key_scoped\" or \"off\", got %q", c.SessionRef)
	}
	switch c.AutoMode {
	case "", "on", "off":
	default:
		return fmt.Errorf("config: audit.client_evidence.auto_mode must be \"on\" or \"off\", got %q", c.AutoMode)
	}
	return nil
}

func validateTraceLinkTemplate(tpl string) error {
	if tpl == "" {
		return nil
	}
	if !strings.Contains(tpl, "{trace_id}") {
		return fmt.Errorf("config: otel.trace_link_template must contain {trace_id}")
	}
	u, err := url.Parse(strings.ReplaceAll(tpl, "{trace_id}", strings.Repeat("0", 32)))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("config: otel.trace_link_template must be an http or https URL with a host")
	}
	return nil
}
```

Call `validateTraceLinkTemplate(o.TraceLinkTemplate)` at the end of `validateOTel` (before `return nil`), and add `validateClientEvidence(cfg.Audit.ClientEvidence)` next to the `validateOTel(cfg.OTel)` call in the load path (near `config.go:815`). Add `net/url` to the imports if absent.

Run the Step 5 command again. Expected: `ok`. Also run `go test ./internal/config/ > …t3c.log 2>&1; grep -E '^(ok|FAIL)'` → `ok` (no regressions).

- [ ] **Step 7: Write the failing data-plane wiring test**

`internal/server/client_evidence_test.go`:

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/clientctx"
)

// The option must default to DefaultMode and wrap inside KeyAuth.
func TestDataMuxOptionsDefaultClientEvidence(t *testing.T) {
	o := newDataMuxOptions()
	if o.clientEvidence != clientctx.DefaultMode() {
		t.Fatalf("default = %+v", o.clientEvidence)
	}
	WithClientEvidence(clientctx.Mode{})(&o)
	if o.clientEvidence != (clientctx.Mode{}) {
		t.Fatalf("option not applied: %+v", o.clientEvidence)
	}
}

func TestEvidenceWrapperAttachesCollector(t *testing.T) {
	var ok bool
	h := clientctx.Middleware(clientctx.DefaultMode(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := audit.Record{Event: "request_completed", Outcome: &audit.OutcomeRef{Status: 200}}
		audit.Finish(r.Context(), &rec)
		ok = rec.Latency != nil
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil))
	if !ok {
		t.Fatal("no evidence collector attached")
	}
}
```

Run: `go test ./internal/server/ -run 'ClientEvidence|EvidenceWrapper' > /home/atomoh/.tmp/inferplane-audit-console-20261029/t3s.log 2>&1; grep -E '^(ok|FAIL|---|.*undefined)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t3s.log`
Expected: FAIL `undefined: newDataMuxOptions`.

- [ ] **Step 8: Wire the option**

In `internal/server/server.go`:
- add `clientEvidence clientctx.Mode` to `dataMuxOptions`;
- add

```go
func newDataMuxOptions() dataMuxOptions { return dataMuxOptions{clientEvidence: clientctx.DefaultMode()} }

// WithClientEvidence selects ADR-057 client evidence (default: both on).
func WithClientEvidence(m clientctx.Mode) DataMuxOption {
	return func(o *dataMuxOptions) { o.clientEvidence = m }
}
```

- in `DataMux`, initialise the options with `o := newDataMuxOptions()` instead of the zero value (keep the existing `for _, opt := range opts { opt(&o) }` loop);
- change the final mount to `dataMux.Handle("/", maxBytesMiddleware(limit, KeyAuth(store, clientctx.Middleware(o.clientEvidence, governed))))`.

In `cmd/mayu/gateway.go` add

```go
// clientEvidenceMode maps audit.client_evidence to the data-plane option.
func clientEvidenceMode(cfg *config.Config) clientctx.Mode {
	m := clientctx.DefaultMode()
	if ce := cfg.Audit.ClientEvidence; ce != nil {
		m.SessionRef = ce.SessionRef != "off"
		m.AutoMode = ce.AutoMode != "off"
	}
	return m
}
```

and append `server.WithClientEvidence(clientEvidenceMode(cfg))` to the `server.DataMux(...)` options at `gateway.go:926`.

- [ ] **Step 9: Run the package tests**

Run: `go test ./internal/server/ ./internal/clientctx/ ./internal/config/ ./cmd/mayu/ -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t3s.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t3s.log`
Expected: four `ok` lines.

- [ ] **Step 10: Commit**

```bash
git add internal/clientctx internal/config/config.go internal/config/audit_console_test.go internal/server/server.go internal/server/client_evidence_test.go cmd/mayu/gateway.go
git commit -s -m "feat: capture bounded client evidence on the data plane (ADR-057)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Messages ingress evidence

**Files:**
- Create: `internal/server/requestpolicy/attempt.go`
- Modify: `internal/server/anthropicapi/messages.go` (`audit`, `auditCompleted`, `auditCompletedPartial`, fallback loop, `serveComplete`, `serveStream`)
- Test: `internal/server/anthropicapi/audit_evidence_test.go`

**Interfaces:**
- Consumes: `audit.Finish`, `audit.BeginAttempt`, `audit.NoteAttemptHTTP`, `audit.MarkFirstToken`, attempt status constants (Task 2); `clientctx.FromRequest`, `clientctx.WithMode`, `clientctx.DefaultMode` (Task 3, tests only).
- Produces: `func requestpolicy.NoteAttemptFailure(ctx context.Context, err error)` (records the upstream status carried by a `*providers.UpstreamError`); `func requestpolicy.RetryStatus(crossModelNext bool) string`.

- [ ] **Step 1: Write the failing tests**

`internal/server/anthropicapi/audit_evidence_test.go`:

```go
package anthropicapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/clientctx"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

const rawSession = "raw-session-6f1d2c"

func evidenceWriter(t *testing.T) (*audit.Writer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	return w, &buf
}

func evidenceRequest(body string, p keystore.Principal) *http.Request {
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-claude-code-request-class", "main")
	req.Header.Set("X-Claude-Code-Session-Id", rawSession)
	ctx := principal.With(req.Context(), p)
	ctx = audit.WithEvidence(ctx, time.Now(), clientctx.FromRequest(req.Header, p.KeyID, clientctx.DefaultMode()))
	return req.WithContext(clientctx.WithMode(ctx, clientctx.DefaultMode()))
}

func records(t *testing.T, buf *bytes.Buffer) []audit.Record {
	t.Helper()
	var out []audit.Record
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var r audit.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func terminal(t *testing.T, recs []audit.Record) audit.Record {
	t.Helper()
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Event == "request_completed" || (recs[i].Event == "request_started" && recs[i].Outcome != nil) {
			return recs[i]
		}
	}
	t.Fatal("no terminal record")
	return audit.Record{}
}

func fallbackRouter() *router.Router {
	provs := map[string]providers.Provider{
		"bad":  statusProvider{code: 503, body: []byte(`{"type":"error"}`)},
		"good": mockprovider.New("claude-sonnet-4-6"),
	}
	models := map[string]config.ModelConfig{"claude-sonnet-4-6": {Targets: []config.Target{
		{Provider: "bad", Model: "m1"}, {Provider: "good", Model: "claude-sonnet-4-6"},
	}}}
	return router.New(holderFor(provs, models))
}

func TestMessagesFallbackRecordsAttempts(t *testing.T) {
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerFull(fallbackRouter(), w, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, evidenceRequest(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_e", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	done := terminal(t, records(t, buf))
	if len(done.Attempts) != 2 || done.Attempts[0].Provider != "bad" || done.Attempts[0].Status != audit.AttemptUpstreamError ||
		done.Attempts[0].HTTP != 503 || done.Attempts[1].Provider != "good" || done.Attempts[1].Status != audit.AttemptOK {
		t.Fatalf("attempts = %+v", done.Attempts)
	}
	if !done.Outcome.FallbackUsed || strings.Join(done.Outcome.FallbackChain, ",") != "bad,good" {
		t.Fatalf("outcome = %+v", done.Outcome)
	}
	if done.Latency == nil || done.Client == nil || done.Client.RequestClass != "main" || !strings.HasPrefix(done.Client.SessionRef, "s1:") {
		t.Fatalf("latency=%+v client=%+v", done.Latency, done.Client)
	}
}

func TestMessagesDenialCarriesClientAndLatency(t *testing.T) {
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerWithAudit(testRouter(), w)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, evidenceRequest(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_d", Team: "t", AllowedModels: []string{"qwen-coder"}}))
	w.Close()
	denied := terminal(t, records(t, buf))
	if denied.Event != "request_started" || denied.Outcome.Status != 403 || denied.Client == nil || denied.Latency == nil || len(denied.Attempts) != 0 {
		t.Fatalf("denied = %+v", denied)
	}
}

func TestMessagesStreamMarksFirstToken(t *testing.T) {
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerFull(testRouter(), w, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, evidenceRequest(`{"model":"claude-sonnet-4-6","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_s", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	done := terminal(t, records(t, buf))
	if done.Latency == nil || done.Latency.TTFTMs < 1 || len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptOK {
		t.Fatalf("stream record = %+v", done)
	}
}

func TestMessagesRawSessionIDNeverAudited(t *testing.T) {
	w, buf := evidenceWriter(t)
	h := NewMessagesHandlerFull(testRouter(), w, nil)
	h.ServeHTTP(httptest.NewRecorder(), evidenceRequest(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		keystore.Principal{KeyID: "ik_r", Team: "t", AllowedModels: []string{"*"}}))
	w.Close()
	if strings.Contains(buf.String(), rawSession) {
		t.Fatal("raw session ID reached the audit chain")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/anthropicapi/ -run 'Messages(Fallback|Denial|Stream|RawSession)' > /home/atomoh/.tmp/inferplane-audit-console-20261029/t4.log 2>&1; grep -E '^(ok|FAIL|---|    )' /home/atomoh/.tmp/inferplane-audit-console-20261029/t4.log`
Expected: FAIL — `attempts = []` (and `latency=<nil>`).

- [ ] **Step 3: Add the shared attempt helper**

`internal/server/requestpolicy/attempt.go`:

```go
package requestpolicy

import (
	"context"
	"errors"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/providers"
)

// NoteAttemptFailure records the upstream HTTP status of a retriable failure
// on the open attempt (ADR-057). Transport errors carry no status.
func NoteAttemptFailure(ctx context.Context, err error) {
	var ue *providers.UpstreamError
	if errors.As(err, &ue) {
		audit.NoteAttemptHTTP(ctx, ue.HTTPStatus())
	}
}

// RetryStatus is the closed attempt status for a retriable failure.
func RetryStatus(crossModelNext bool) string {
	if crossModelNext {
		return audit.AttemptModelNotFound
	}
	return audit.AttemptUpstreamError
}
```

- [ ] **Step 4: Wire Messages**

In `internal/server/anthropicapi/messages.go`:
1. In `audit`, `auditCompleted` and `auditCompletedPartial`, insert `audit.Finish(ctx, &rec)` immediately before `h.aud.Append(rec)`.
2. In the fallback loop (`ServeHTTP`, after `attemptReq = requestpolicy.WithAffinityAttempt(...)` and before `var retriable bool`) add
   `endAttempt := audit.BeginAttempt(attemptReq.Context(), ct.ProviderName, ct.Model)`.
3. After `h.r.RecordResult(ct.ProviderName, ct.Identity, false)` in the same loop add `endAttempt(requestpolicy.RetryStatus(crossModelNext), 0)`.
4. In `serveComplete`, change the first retriable return to

```go
		if !last && retryMessagesError(err, crossModelNext) {
			requestpolicy.NoteAttemptFailure(req.Context(), err)
			return true // transport error → fall back
		}
```

   and the status-based one to

```go
	if !last && (resp.StatusCode >= 500 || resp.StatusCode == 429 || (crossModelNext && isModelNotFound(resp.StatusCode, resp.RawBody))) {
		audit.NoteAttemptHTTP(req.Context(), resp.StatusCode)
		return true
	}
```
5. In `serveStream`, change the pre-TTFT retriable return to call `requestpolicy.NoteAttemptFailure(req.Context(), err)` before `return true`, and change

```go
		if ttft == 0 {
			ttft = time.Since(start).Seconds() // first streamed event = time to first token
		}
```

   to

```go
		if ttft == 0 {
			ttft = time.Since(start).Seconds() // first streamed event = time to first token
			audit.MarkFirstToken(req.Context())
		}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/server/anthropicapi/ ./internal/server/requestpolicy/ -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t4.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t4.log`
Expected: two `ok` lines (existing tests unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/server/requestpolicy/attempt.go internal/server/anthropicapi/messages.go internal/server/anthropicapi/audit_evidence_test.go
git commit -s -m "feat(anthropicapi): record attempts, latency and client evidence

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Chat and Invoke/Converse ingress evidence

**Files:**
- Modify: `internal/server/openaiapi/chat.go`, `internal/server/bedrockapi/invoke.go`
- Test: `internal/server/openaiapi/audit_evidence_test.go`, `internal/server/bedrockapi/audit_evidence_test.go`

**Interfaces:**
- Consumes: Task 2 collector functions; `requestpolicy.NoteAttemptFailure`, `requestpolicy.RetryStatus` (Task 4).

- [ ] **Step 1: Write the failing tests**

`internal/server/openaiapi/audit_evidence_test.go` (uses the package's existing `statusProvider`, `holderFor`, `testRouter` from `chat_test.go`):

```go
package openaiapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/clientctx"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
	"github.com/inferplane/inferplane/providers/testing/mockprovider"
)

func chatWriter(t *testing.T) (*audit.Writer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	return w, &buf
}

func chatServe(h http.Handler, body string, p keystore.Principal) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("x-claude-code-request-class", "subagent")
	ctx := audit.WithEvidence(principal.With(req.Context(), p), time.Now(), clientctx.FromRequest(req.Header, p.KeyID, clientctx.DefaultMode()))
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(clientctx.WithMode(ctx, clientctx.DefaultMode())))
}

func chatTerminal(t *testing.T, buf *bytes.Buffer) audit.Record {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var r audit.Record
		if json.Unmarshal([]byte(lines[i]), &r) == nil && (r.Event == "request_completed" || (r.Event == "request_started" && r.Outcome != nil)) {
			return r
		}
	}
	t.Fatal("no terminal record")
	return audit.Record{}
}

func TestChatFallbackRecordsAttempts(t *testing.T) {
	provs := map[string]providers.Provider{"bad": statusProvider{code: 503, body: []byte(`{"error":{}}`)}, "good": mockprovider.New("claude-sonnet-4-6")}
	models := map[string]config.ModelConfig{"claude-sonnet-4-6": {Targets: []config.Target{{Provider: "bad", Model: "m1"}, {Provider: "good", Model: "claude-sonnet-4-6"}}}}
	w, buf := chatWriter(t)
	chatServe(NewChatHandlerFull(router.New(holderFor(provs, models)), w, nil),
		`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}})
	w.Close()
	done := chatTerminal(t, buf)
	if len(done.Attempts) != 2 || done.Attempts[0].HTTP != 503 || done.Attempts[1].Status != audit.AttemptOK || !done.Outcome.FallbackUsed || done.Latency == nil {
		t.Fatalf("chat record = %+v", done)
	}
}

func TestChatDenialCarriesClient(t *testing.T) {
	w, buf := chatWriter(t)
	chatServe(NewChatHandlerFull(testRouter(), w, nil),
		`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`, keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"other"}})
	w.Close()
	d := chatTerminal(t, buf)
	if d.Event != "request_started" || d.Client == nil || d.Client.RequestClass != "subagent" || d.Latency == nil || len(d.Attempts) != 0 {
		t.Fatalf("denial = %+v", d)
	}
}
```

`internal/server/bedrockapi/audit_evidence_test.go` (uses `statusProvider`, `captureProvider`, `streamProvider` from `invoke_test.go` and `holderFor` from `resolve_test.go`; their `Name()` is `"mock"`, which passes the Bedrock-ingress filter):

```go
package bedrockapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/clientctx"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/providers"
)

const invokeBody = `{"anthropic_version":"bedrock-2023-05-31","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`

func invokeWriter(t *testing.T) (*audit.Writer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	return w, &buf
}

func invokeServe(h http.Handler, path string, p keystore.Principal) {
	req := httptest.NewRequest("POST", path, strings.NewReader(invokeBody))
	req.Header.Set("x-claude-code-request-class", "main")
	ctx := audit.WithEvidence(principal.With(req.Context(), p), time.Now(), clientctx.FromRequest(req.Header, p.KeyID, clientctx.DefaultMode()))
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(clientctx.WithMode(ctx, clientctx.DefaultMode())))
}

func invokeTerminal(t *testing.T, buf *bytes.Buffer) audit.Record {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var r audit.Record
		if json.Unmarshal([]byte(lines[i]), &r) == nil && (r.Event == "request_completed" || (r.Event == "request_started" && r.Outcome != nil)) {
			return r
		}
	}
	t.Fatal("no terminal record")
	return audit.Record{}
}

var anyModel = keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}}

func TestInvokeFallbackRecordsAttempts(t *testing.T) {
	provs := map[string]providers.Provider{"bad": statusProvider{code: 503, body: []byte(`{"message":"busy"}`)}, "good": &captureProvider{}}
	models := map[string]config.ModelConfig{"claude-x": {Targets: []config.Target{{Provider: "bad", Model: "m1"}, {Provider: "good", Model: "claude-x"}}}}
	w, buf := invokeWriter(t)
	h := holderFor(provs, models)
	invokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, false), "/model/claude-x/invoke", anyModel)
	w.Close()
	done := invokeTerminal(t, buf)
	if len(done.Attempts) != 2 || done.Attempts[0].HTTP != 503 || done.Attempts[1].Status != audit.AttemptOK || !done.Outcome.FallbackUsed {
		t.Fatalf("invoke record = %+v", done)
	}
}

func TestInvokeDenialCarriesClient(t *testing.T) {
	provs := map[string]providers.Provider{"p": &captureProvider{}}
	models := map[string]config.ModelConfig{"claude-x": {Targets: []config.Target{{Provider: "p", Model: "claude-x"}}}}
	w, buf := invokeWriter(t)
	h := holderFor(provs, models)
	invokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, false), "/model/claude-x/invoke",
		keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"other"}})
	w.Close()
	d := invokeTerminal(t, buf)
	if d.Event != "request_started" || d.Outcome.Status != 403 || d.Client == nil || d.Latency == nil {
		t.Fatalf("denial = %+v", d)
	}
}

func TestInvokeStreamRecordsAttempt(t *testing.T) {
	provs := map[string]providers.Provider{"p": streamProvider{}}
	models := map[string]config.ModelConfig{"claude-x": {Targets: []config.Target{{Provider: "p", Model: "claude-x"}}}}
	w, buf := invokeWriter(t)
	h := holderFor(provs, models)
	invokeServe(NewInvokeHandlerMetrics(router.New(h), h, w, nil, nil, true), "/model/claude-x/invoke-with-response-stream", anyModel)
	w.Close()
	done := invokeTerminal(t, buf)
	if len(done.Attempts) != 1 || done.Attempts[0].Status != audit.AttemptOK || done.Latency == nil {
		t.Fatalf("stream record = %+v", done)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/openaiapi/ ./internal/server/bedrockapi/ -run 'Evidence|Fallback|Denial|FirstToken' > /home/atomoh/.tmp/inferplane-audit-console-20261029/t5.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t5.log`
Expected: FAIL on the new tests.

- [ ] **Step 3: Wire Chat and Invoke**

Apply the same five edits as Task 4 Step 4 to each handler:
1. `audit.Finish(ctx, &rec)` before `h.aud.Append(rec)` in `audit`, `auditCompleted`, `auditCompletedPartial` (`chat.go:723-800`, `invoke.go:734-815`).
2. `endAttempt := audit.BeginAttempt(attemptReq.Context(), ct.ProviderName, ct.Model)` before the serve call in the loop (`chat.go:~352`, `invoke.go:~383`).
3. `endAttempt(requestpolicy.RetryStatus(crossModelNext), 0)` after the retriable `RecordResult` (`chat.go:~366`, `invoke.go:~397`).
4. In `serveComplete` and `serveStream`, every `return true` gains `requestpolicy.NoteAttemptFailure(req.Context(), err)` (error paths) or `audit.NoteAttemptHTTP(req.Context(), resp.StatusCode)` (status paths) just before it.
5. Each `ttft = time.Since(start).Seconds()` assignment (`chat.go:527`, `invoke.go:594` and `invoke.go:612`) is followed by `audit.MarkFirstToken(req.Context())`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/server/openaiapi/ ./internal/server/bedrockapi/ -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t5.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t5.log`
Expected: two `ok` lines.

- [ ] **Step 5: Commit**

```bash
git add internal/server/openaiapi internal/server/bedrockapi
git commit -s -m "feat: record attempts and latency on chat and Bedrock ingress

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Responses ingress evidence

**Files:**
- Modify: `internal/server/responsesapi/handler.go` (loop), `internal/server/responsesapi/observe.go` (`denied`, `finish`)
- Test: `internal/server/responsesapi/audit_evidence_test.go`

**Interfaces:**
- Consumes: Task 2 collector functions.

- [ ] **Step 1: Write the failing tests**

`internal/server/responsesapi/audit_evidence_test.go` reuses `newStreamFixture` from `stream_failure_test.go` (two probe providers, `first` then `fallback`; `serve` returns every `request_completed`):

```go
package responsesapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/providers"
)

func TestResponsesFallbackRowsCarryCumulativeAttempts(t *testing.T) {
	f := newStreamFixture(t)
	f.first.openErr = &providers.UpstreamError{StatusCode: 503, Body: []byte(`{"error":{"code":"overloaded"}}`)}
	ctx := audit.WithEvidence(context.Background(), time.Now(), &audit.ClientRef{RequestClass: "main"})
	records := f.serve(t, httptest.NewRecorder(), ctx)
	if len(records) != 2 {
		t.Fatalf("want one completed record per attempt, got %d", len(records))
	}
	first, second := records[0], records[1]
	if first.Outcome.Status != 503 || len(first.Attempts) != 1 || first.Attempts[0].Status != audit.AttemptUpstreamError || first.Attempts[0].HTTP != 503 {
		t.Fatalf("first row = %+v", first)
	}
	if len(second.Attempts) != 2 || second.Attempts[1].Status != audit.AttemptOK || !second.Outcome.FallbackUsed || second.Client == nil {
		t.Fatalf("second row = %+v", second)
	}
}

func TestResponsesDenialCarriesClient(t *testing.T) {
	f := newStreamFixture(t)
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"premium","input":"hello"}`))
	p := keystore.Principal{Team: "team", KeyID: "k", AllowedModels: []string{"other"}}
	ctx := audit.WithEvidence(principal.With(req.Context(), p), time.Now(), &audit.ClientRef{RequestClass: "main"})
	f.h.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))
	f.close(t)
	var denied *audit.Record
	sc := bufio.NewScanner(&f.audit)
	for sc.Scan() {
		var r audit.Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Event == "request_started" && r.Outcome != nil {
			denied = &r
		}
	}
	if denied == nil || denied.Client == nil || denied.Latency == nil {
		t.Fatalf("denied record = %+v", denied)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/responsesapi/ -run 'Responses(Fallback|Denial)' > /home/atomoh/.tmp/inferplane-audit-console-20261029/t6.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t6.log`
Expected: FAIL (`attempts` empty).

- [ ] **Step 3: Wire Responses**

1. `observe.go` `denied`: build the record into a variable, call `audit.Finish(req.Context(), &rec)`, then append:

```go
	if h.aud != nil {
		rec := audit.Record{
			SchemaVersion: 1, Event: "request_started", ID: ulid.New(), TS: time.Now().UTC().Format(time.RFC3339Nano),
			Principal: requestpolicy.AuditPrincipal(p),
			Request:   audit.RequestRef{Ingress: "responses", ModelRequested: model, Routing: audit.RoutingFrom(req.Context())},
			Outcome:   &audit.OutcomeRef{Status: status, Error: &reason},
		}
		audit.Finish(req.Context(), &rec)
		h.aud.Append(rec)
	}
```
2. `observe.go` `finish`: insert `audit.Finish(a.req.Context(), &rec)` before `a.h.aud.Append(rec)`. Its own `Latency` stays (Finish keeps an existing one).
3. `handler.go` loop: before `if stream {` add `endAttempt := audit.BeginAttempt(attemptReq.Context(), ct.ProviderName, ct.Model)`; after `h.r.RecordResult(ct.ProviderName, ct.Identity, false)` add `endAttempt(audit.AttemptUpstreamError, 0)` (a no-op when `finish` already closed it).

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/server/responsesapi/ ./internal/server/ -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t6.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t6.log`
Expected: two `ok` lines.

- [ ] **Step 5: Commit**

```bash
git add internal/server/responsesapi
git commit -s -m "feat(responsesapi): record cumulative attempts and client evidence

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Auto mode verdict detection

Uses Task 1's findings; apply any Task 1 ruling to the constants and the table below before Step 1.

**Files:**
- Create: `internal/clientctx/automode.go`, `internal/clientctx/automode_test.go`
- Modify: `internal/metrics/metrics.go` (counter + `ObserveAutoModeUnparsed`)
- Modify: `internal/server/anthropicapi/messages.go`, `internal/server/bedrockapi/invoke.go` (tap in complete and stream paths)
- Test: `internal/server/anthropicapi/automode_wire_test.go`

**Interfaces:**
- Consumes: `clientctx.AutoModeWanted` (Task 3), `audit.SetAutoMode` (Task 2).
- Produces: `type clientctx.AutoModeTap`, `func clientctx.NewAutoModeTap() *AutoModeTap`, methods `ObserveResponse(*schema.ChatResponse)`, `ObserveChunk(*schema.ChatChunk)`, `Result(requestStops []string) (*audit.AutoModeRef, bool)`; `func clientctx.RequestStopSequences(*schema.ChatRequest) []string`; constants `VerdictBlock`, `VerdictAllow`, `VerdictUnknown`, `StageS1`, `StageS2`, `AutoModeSignature`; `func (*metrics.Metrics) ObserveAutoModeUnparsed()`.

- [ ] **Step 1: Write the failing detector tests**

`internal/clientctx/automode_test.go`:

```go
package clientctx

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/pkg/schema"
)

func resp(text, stopReason, stopSeq string) *schema.ChatResponse {
	r := &schema.ChatResponse{Content: []schema.ContentBlock{{Type: "text", Text: &text}}}
	if stopReason != "" {
		r.StopReason = &stopReason
	}
	if stopSeq != "" {
		r.StopSequence = &stopSeq
	}
	return r
}

func TestAutoModeVerdicts(t *testing.T) {
	s1 := []string{"</block>"}
	cases := []struct {
		name          string
		stops         []string
		r             *schema.ChatResponse
		like          bool
		verdict, stage string
	}{
		{"stage1 block", s1, resp("<block>yes", "stop_sequence", "</block>"), true, VerdictBlock, StageS1},
		{"stage1 allow", s1, resp("<block>no", "stop_sequence", "</block>"), true, VerdictAllow, StageS1},
		{"stage1 wrong stop", s1, resp("<block>yes", "end_turn", ""), true, VerdictUnknown, StageS1},
		{"stage2 with thinking", nil, resp("<thinking>maybe <block>no</block></thinking>\n<block>yes</block>", "end_turn", ""), true, VerdictBlock, StageS2},
		{"conflicting blocks", nil, resp("<block>yes</block> <block>no</block>", "end_turn", ""), true, VerdictUnknown, StageS2},
		{"block only inside thinking", nil, resp("<thinking><block>yes</block></thinking>", "end_turn", ""), true, VerdictUnknown, StageS2},
		{"not a classifier", nil, resp("hello", "end_turn", ""), false, "", ""},
	}
	for _, c := range cases {
		tap := NewAutoModeTap()
		tap.ObserveResponse(c.r)
		ref, like := tap.Result(c.stops)
		if like != c.like {
			t.Fatalf("%s: like=%v", c.name, like)
		}
		if !like {
			continue
		}
		if ref.Verdict != c.verdict || ref.Stage != c.stage || ref.Signature != AutoModeSignature {
			t.Fatalf("%s: ref=%+v", c.name, ref)
		}
	}
}

func TestAutoModeStreamDeltas(t *testing.T) {
	tap := NewAutoModeTap()
	for _, c := range []schema.ChatChunk{
		{Type: "content_block_delta", Delta: json.RawMessage(`{"type":"text_delta","text":"<blo"}`)},
		{Type: "content_block_delta", Delta: json.RawMessage(`{"type":"text_delta","text":"ck>yes"}`)},
		{Type: "message_delta", Delta: json.RawMessage(`{"stop_reason":"stop_sequence","stop_sequence":"</block>"}`)},
	} {
		tap.ObserveChunk(&c)
	}
	ref, like := tap.Result([]string{"</block>"})
	if !like || ref.Verdict != VerdictBlock {
		t.Fatalf("stream ref=%+v like=%v", ref, like)
	}
}

func TestAutoModeTextBound(t *testing.T) {
	tap := NewAutoModeTap()
	tap.ObserveResponse(resp(strings.Repeat("x", 17<<10)+"<block>yes</block>", "end_turn", ""))
	ref, like := tap.Result(nil)
	if like && ref.Verdict != VerdictUnknown {
		t.Fatalf("over-bound text must not yield a verdict: %+v", ref)
	}
}

func TestRequestStopSequences(t *testing.T) {
	var req schema.ChatRequest
	if err := json.Unmarshal([]byte(`{"model":"m","messages":[],"stop_sequences":["</block>"]}`), &req); err != nil {
		t.Fatal(err)
	}
	if got := RequestStopSequences(&req); len(got) != 1 || got[0] != "</block>" {
		t.Fatalf("stops = %v", got)
	}
}

func TestNilTapIsSafe(t *testing.T) {
	var tap *AutoModeTap
	tap.ObserveResponse(resp("<block>yes", "", ""))
	tap.ObserveChunk(&schema.ChatChunk{})
	if ref, like := tap.Result(nil); ref != nil || like {
		t.Fatal("nil tap must report nothing")
	}
}
```

Run: `go test ./internal/clientctx/ -run 'AutoMode|StopSequences|NilTap' > /home/atomoh/.tmp/inferplane-audit-console-20261029/t7.log 2>&1; grep -E '^(ok|FAIL|---|.*undefined)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t7.log`
Expected: FAIL `undefined: NewAutoModeTap`.

- [ ] **Step 2: Implement the detector**

`internal/clientctx/automode.go`:

```go
package clientctx

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/pkg/schema"
)

const (
	// AutoModeSignature names the client response format this parser follows.
	AutoModeSignature = "claude-code-2.1"
	VerdictBlock      = "block"
	VerdictAllow      = "allow"
	VerdictUnknown    = "unknown"
	StageS1           = "s1"
	StageS2           = "s2"

	maxAutoModeText = 16 << 10
	blockStop       = "</block>"
)

var (
	thinkingRE = regexp.MustCompile(`(?s)<thinking>.*?(?:</thinking>|\z)`)
	blockRE    = regexp.MustCompile(`<block>\s*(yes|no)\b`)
)

// AutoModeTap collects at most 16 KiB of assistant text plus the stop fields
// of one response. A nil tap ignores everything.
type AutoModeTap struct {
	text                     strings.Builder
	truncated                bool
	stopReason, stopSequence string
}

func NewAutoModeTap() *AutoModeTap { return &AutoModeTap{} }

func (t *AutoModeTap) addText(s string) {
	room := maxAutoModeText - t.text.Len()
	if room <= 0 {
		t.truncated = t.truncated || s != ""
		return
	}
	if len(s) > room {
		s, t.truncated = s[:room], true
	}
	t.text.WriteString(s)
}

// ObserveResponse reads a non-streaming Anthropic-format response.
func (t *AutoModeTap) ObserveResponse(r *schema.ChatResponse) {
	if t == nil || r == nil {
		return
	}
	for _, b := range r.Content {
		if b.Type == "text" && b.Text != nil {
			t.addText(*b.Text)
		}
	}
	if r.StopReason != nil {
		t.stopReason = *r.StopReason
	}
	if r.StopSequence != nil {
		t.stopSequence = *r.StopSequence
	}
}

// ObserveChunk reads one canonical stream chunk.
func (t *AutoModeTap) ObserveChunk(c *schema.ChatChunk) {
	if t == nil || c == nil || len(c.Delta) == 0 {
		return
	}
	var d struct {
		Type         string  `json:"type"`
		Text         string  `json:"text"`
		StopReason   *string `json:"stop_reason"`
		StopSequence *string `json:"stop_sequence"`
	}
	if json.Unmarshal(c.Delta, &d) != nil {
		return
	}
	switch c.Type {
	case "content_block_delta":
		if d.Type == "text_delta" {
			t.addText(d.Text)
		}
	case "message_delta":
		if d.StopReason != nil {
			t.stopReason = *d.StopReason
		}
		if d.StopSequence != nil {
			t.stopSequence = *d.StopSequence
		}
	}
}

// Result applies Claude Code's own rule: after removing <thinking> blocks,
// exactly one consistent <block>yes|no value; a stage-1 call must also have
// stopped on </block>. The bool reports a classifier-like call; anything
// classifier-like that does not match is VerdictUnknown, never a guess.
func (t *AutoModeTap) Result(requestStops []string) (*audit.AutoModeRef, bool) {
	if t == nil {
		return nil, false
	}
	stage1 := slices.Contains(requestStops, blockStop)
	raw := t.text.String()
	if !stage1 && !strings.Contains(raw, "<block>") {
		return nil, false
	}
	ref := &audit.AutoModeRef{Verdict: VerdictUnknown, Stage: StageS2, Signature: AutoModeSignature}
	if stage1 {
		ref.Stage = StageS1
	}
	if t.truncated || (stage1 && (t.stopReason != "stop_sequence" || t.stopSequence != blockStop)) {
		return ref, true
	}
	verdict := ""
	for _, m := range blockRE.FindAllStringSubmatch(thinkingRE.ReplaceAllString(raw, ""), -1) {
		v := VerdictAllow
		if m[1] == "yes" {
			v = VerdictBlock
		}
		if verdict != "" && verdict != v {
			return ref, true
		}
		verdict = v
	}
	if verdict != "" {
		ref.Verdict = verdict
	}
	return ref, true
}

// RequestStopSequences reads stop_sequences, which the canonical request keeps in Extra.
func RequestStopSequences(req *schema.ChatRequest) []string {
	if req == nil {
		return nil
	}
	raw, ok := req.Extra["stop_sequences"]
	if !ok {
		return nil
	}
	var out []string
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}
```

Run the Step 1 command. Expected: `ok`.

- [ ] **Step 3: Add the metric**

In `internal/metrics/metrics.go`: add field `autoModeUnparsed prometheus.Counter // inferplane_audit_auto_mode_unparsed_total`; in `New()` initialise

```go
		autoModeUnparsed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "inferplane_audit_auto_mode_unparsed_total",
			Help: "Auxiliary responses that looked like Claude Code auto mode classifier calls but did not parse (ADR-057; no labels).",
		}),
```

append `m.autoModeUnparsed` to the `reg.MustRegister(` call, and add

```go
// ObserveAutoModeUnparsed counts a classifier-like response with no verdict.
func (m *Metrics) ObserveAutoModeUnparsed() {
	if m == nil {
		return
	}
	m.autoModeUnparsed.Inc()
}
```

- [ ] **Step 4: Write the failing wire test**

`internal/server/anthropicapi/automode_wire_test.go` uses a provider that answers `<block>yes` with `stop_sequence` `</block>`:

```go
package anthropicapi

import (
	"bytes"
	"context"
	"iter"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/clientctx"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/principal"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

type verdictProvider struct{}

func (verdictProvider) Name() string               { return "verdict" }
func (verdictProvider) Models() []schema.ModelInfo { return nil }
func (verdictProvider) Complete(context.Context, *providers.ProxyRequest) (*providers.ProxyResponse, error) {
	text, stop, seq := "<block>yes", "stop_sequence", "</block>"
	in, out := int64(5), int64(2)
	body := []byte(`{"id":"msg_v","type":"message","role":"assistant","content":[{"type":"text","text":"<block>yes"}],"stop_reason":"stop_sequence","stop_sequence":"</block>","usage":{"input_tokens":5,"output_tokens":2}}`)
	return &providers.ProxyResponse{StatusCode: 200, RawBody: body, Parsed: &schema.ChatResponse{
		Content: []schema.ContentBlock{{Type: "text", Text: &text}}, StopReason: &stop, StopSequence: &seq,
		Usage: &schema.Usage{InputTokens: &in, OutputTokens: &out}}}, nil
}
func (verdictProvider) Stream(context.Context, *providers.ProxyRequest) (iter.Seq2[*providers.StreamEvent, error], error) {
	return nil, nil
}

func TestMessagesRecordsAutoModeVerdict(t *testing.T) {
	var buf bytes.Buffer
	w, err := audit.NewWriter("i", filepath.Join(t.TempDir(), "a.wal"), []audit.Sink{audit.NewWriterSink("b", &buf, true)})
	if err != nil {
		t.Fatal(err)
	}
	models := map[string]config.ModelConfig{"claude-sonnet-5": {Targets: []config.Target{{Provider: "v", Model: "claude-sonnet-5"}}}}
	h := NewMessagesHandlerFull(router.New(holderFor(map[string]providers.Provider{"v": verdictProvider{}}, models)), w, nil)
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5","max_tokens":64,"stop_sequences":["</block>"],"messages":[{"role":"user","content":"classify"}]}`))
	req.Header.Set("x-claude-code-request-class", "auxiliary")
	p := keystore.Principal{KeyID: "ik", Team: "t", AllowedModels: []string{"*"}}
	ctx := audit.WithEvidence(principal.With(req.Context(), p), time.Now(), clientctx.FromRequest(req.Header, p.KeyID, clientctx.DefaultMode()))
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(clientctx.WithMode(ctx, clientctx.DefaultMode())))
	w.Close()
	if !strings.Contains(buf.String(), `"auto_mode":{"verdict":"block","stage":"s1","signature":"claude-code-2.1"}`) {
		t.Fatalf("auto_mode missing: %s", buf.String())
	}
	if strings.Contains(buf.String(), "<block>") {
		t.Fatal("response text reached the audit chain")
	}
}
```

Run: `go test ./internal/server/anthropicapi/ -run AutoModeVerdict > …/t7w.log 2>&1; grep -E '^(ok|FAIL|---)' …/t7w.log` → FAIL (`auto_mode missing`).

- [ ] **Step 5: Hook the tap into Messages and Invoke**

Add one method per handler (shown for Messages; Invoke is identical with `InvokeHandler`):

```go
// recordAutoMode stores an estimated classifier verdict before the terminal
// audit record is built (ADR-057). Response bytes are never changed.
func (h *MessagesHandler) recordAutoMode(ctx context.Context, tap *clientctx.AutoModeTap, parsed *schema.ChatRequest) {
	ref, like := tap.Result(clientctx.RequestStopSequences(parsed))
	if !like {
		return
	}
	audit.SetAutoMode(ctx, ref)
	if ref.Verdict == clientctx.VerdictUnknown {
		h.metrics.ObserveAutoModeUnparsed()
	}
}
```

- `serveComplete`: before the final `h.auditCompleted(req.Context(), recID, …)` add

```go
	if clientctx.AutoModeWanted(req.Context()) && resp.StatusCode/100 == 2 {
		tap := clientctx.NewAutoModeTap()
		tap.ObserveResponse(resp.Parsed)
		h.recordAutoMode(req.Context(), tap, pr.Parsed)
	}
```
- `serveStream`: before the `for ev, err := range seq` loop add `var tap *clientctx.AutoModeTap; if clientctx.AutoModeWanted(req.Context()) { tap = clientctx.NewAutoModeTap() }`; inside the loop where `ev.Chunk != nil` is handled add `tap.ObserveChunk(ev.Chunk)`; before the final successful `h.auditCompleted(req.Context(), recID, …, 200, …)` add `if tap != nil { h.recordAutoMode(req.Context(), tap, pr.Parsed) }`.

For Invoke, the same three insertions go into `invoke.go` `serveComplete` (before its final `auditCompleted` at `invoke.go:504`) and `serveStream` (tap observes `ev.Chunk` in both chunk branches at `invoke.go:590-628`; record before `invoke.go:649`).

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/clientctx/ ./internal/metrics/ ./internal/server/anthropicapi/ ./internal/server/bedrockapi/ -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t7.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t7.log`
Expected: four `ok` lines.

- [ ] **Step 7: Commit**

```bash
git add internal/clientctx/automode.go internal/clientctx/automode_test.go internal/metrics/metrics.go internal/server/anthropicapi internal/server/bedrockapi
git commit -s -m "feat: record estimated auto mode classifier verdicts (ADR-057)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Audit event projection and query library (Mode A)

**Files:**
- Create: `internal/analytics/auditevents.go` (projection, kind, waterfall), `internal/analytics/auditquery.go` (dialect, SQL, runners, types), `internal/analytics/auditevents_sqlite.go` (`*Index` methods)
- Modify: `internal/analytics/index.go` (`OpenSQLite` DDL, `Replay`), `internal/analytics/sink.go` (carry lines)
- Test: `internal/analytics/auditevents_test.go`

**Interfaces:**
- Consumes: Task 2 record fields, `audit.KnownDenyReason`.
- Produces (all in package `analytics`):
  - `func AuditObservable(r audit.Record) bool`
  - `func KindOf(r audit.Record) string` and constants `KindOK, KindPolicyDenied, KindPrivacyBlocked, KindPrivacyMasked, KindPrivacyInternal, KindUpstreamError, KindAutoModeBlocked`; `var AuditKinds []string`
  - `type AuditEvent struct{…}` (JSON row), `type AuditEventRow struct{ AuditEvent; TSMs int64; RecordJSON string }`
  - `func ProjectAuditEvent(r audit.Record, line []byte) (AuditEventRow, bool)`
  - `type Waterfall`, `type WaterfallRow`, `func WaterfallOf(r audit.Record) Waterfall`
  - `type AuditFilter struct{ Kinds []string; Team, KeyID, Model, Provider, Code, Category, Class, Session, TraceID string; PII, AutoOnly bool; SinceMs, UntilMs int64; Limit int; Before, After string }`
  - `type AuditEventDetail`, `type AuditSession`, `type AuditSummary` (+ `AuditCount`, `AuditPIICounts`, `AuditDenial`, `AuditAutoCounts`, `AuditBucket`)
  - `type AuditEventQuerier interface { AuditEvents(AuditFilter) ([]AuditEvent, string, error); AuditEvent(id string) (AuditEventDetail, bool, error); AuditSessions(AuditFilter) ([]AuditSession, string, error); AuditSessionEvents(ref string, f AuditFilter) ([]AuditEvent, string, error); AuditSummary(AuditFilter) (AuditSummary, error) }`
  - `type Dialect struct{ Placeholder func(n int) string }`, `var SQLiteDialect, PostgresDialect Dialect`, `const AuditEventsDDL string`
  - `type RowScanner interface{ Scan(dest ...any) error }`, `type QueryFunc func(query string, args []any, each func(RowScanner) error) error`, `type ExecFunc func(query string, args ...any) error`
  - `func InsertAuditEvent(d Dialect, exec ExecFunc, row AuditEventRow) error`
  - `func RunAuditEvents/RunAuditEvent/RunAuditSessions/RunAuditSessionEvents/RunAuditSummary(d Dialect, run QueryFunc, …)`
  - `func (ix *Index) IngestAudit(r audit.Record, line []byte) error`

- [ ] **Step 1: Write the failing tests**

`internal/analytics/auditevents_test.go`:

```go
package analytics

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
)

func str(s string) *string { return &s }

func rec(id, event string, status int, mutate func(*audit.Record)) audit.Record {
	r := audit.Record{SchemaVersion: 1, Event: event, ID: id, TS: "2026-10-29T01:00:00Z",
		Principal: audit.PrincipalRef{KeyID: "ik_a", Team: "platform"}, Request: audit.RequestRef{Ingress: "anthropic", ModelRequested: "claude-sonnet-5"},
		Outcome: &audit.OutcomeRef{Status: status}}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func TestKindOf(t *testing.T) {
	cases := map[string]audit.Record{
		KindOK:              rec("1", "request_completed", 200, nil),
		KindPolicyDenied:    rec("2", "request_started", 429, func(r *audit.Record) { r.Outcome.Error = str("key_rate_limited") }),
		KindPrivacyBlocked:  rec("3", "request_started", 403, func(r *audit.Record) { r.Request.Routing = &audit.RoutingRef{Reason: "sensitive_blocked", Privacy: "block"} }),
		KindPrivacyMasked:   rec("4", "request_completed", 200, func(r *audit.Record) { r.Request.Routing = &audit.RoutingRef{Masked: true} }),
		KindPrivacyInternal: rec("5", "request_completed", 200, func(r *audit.Record) { r.Request.Routing = &audit.RoutingRef{Privacy: "internal_only"} }),
		KindUpstreamError:   rec("6", "request_completed", 504, nil),
		KindAutoModeBlocked: rec("7", "request_completed", 200, func(r *audit.Record) { r.AutoMode = &audit.AutoModeRef{Verdict: "block"} }),
	}
	for want, r := range cases {
		if got := KindOf(r); got != want {
			t.Fatalf("KindOf(%s) = %s, want %s", r.ID, got, want)
		}
	}
	if got := KindOf(rec("8", "request_started", 403, func(r *audit.Record) { r.Outcome.Error = str("model_forbidden") })); got != KindPolicyDenied {
		t.Fatalf("responses model_forbidden = %s", got)
	}
	if got := KindOf(rec("9", "request_started", 400, func(r *audit.Record) { r.Outcome.Error = str("context_window_exceeded") })); got != KindUpstreamError {
		t.Fatalf("context error = %s", got)
	}
}

func TestAuditObservable(t *testing.T) {
	if !AuditObservable(rec("1", "request_completed", 200, nil)) || !AuditObservable(rec("2", "request_started", 403, nil)) {
		t.Fatal("terminal records must be observable")
	}
	admitted := rec("3", "request_started", 0, func(r *audit.Record) { r.Outcome = nil })
	if AuditObservable(admitted) || AuditObservable(audit.Record{Event: "admin_key_created"}) {
		t.Fatal("admitted/admin records must be skipped")
	}
}

func TestWaterfallLegacyRecord(t *testing.T) {
	w := WaterfallOf(rec("1", "request_completed", 200, nil))
	if w.TotalMs != 0 || len(w.Rows) != 0 {
		t.Fatalf("legacy waterfall = %+v", w)
	}
}

func TestWaterfallAttempts(t *testing.T) {
	r := rec("1", "request_completed", 200, func(r *audit.Record) {
		r.Latency = &audit.LatencyRef{TotalMs: 1000, TTFTMs: 300}
		r.Attempts = []audit.AttemptRef{{Provider: "a", Status: "upstream_error", HTTP: 503, StartMs: 10, DurMs: 200}, {Provider: "b", Status: "ok", StartMs: 220, DurMs: 760}}
	})
	w := WaterfallOf(r)
	kinds := []string{}
	for _, row := range w.Rows {
		kinds = append(kinds, row.Kind)
	}
	if strings.Join(kinds, ",") != "gateway,attempt,attempt,settle" || w.TTFTMs != 300 {
		t.Fatalf("waterfall = %+v", w)
	}
	denied := WaterfallOf(rec("2", "request_started", 429, func(r *audit.Record) { r.Latency = &audit.LatencyRef{TotalMs: 4} }))
	if len(denied.Rows) != 1 || denied.Rows[0].Kind != "denied" || denied.Rows[0].DurMs != 4 {
		t.Fatalf("denied waterfall = %+v", denied)
	}
}

func openIndex(t *testing.T) *Index {
	t.Helper()
	ix, err := OpenSQLite(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func TestReplayProjectsCompletedAndDenied(t *testing.T) {
	ix := openIndex(t)
	lines := []string{
		`{"schema_version":1,"event":"request_completed","id":"01JC0000000000000000000001","ts":"2026-10-29T01:00:00Z","instance":"i","principal":{"key_id":"ik_a","team":"platform"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"outcome":{"status":200,"fallback_used":false,"partial":false,"error":null},"trace_id":null,"prev_hash":""}`,
		`{"schema_version":1,"event":"request_started","id":"01JC0000000000000000000002","ts":"2026-10-29T01:00:01Z","instance":"i","principal":{"key_id":"ik_a","team":"platform"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"outcome":{"status":429,"fallback_used":false,"partial":false,"error":"key_rate_limited"},"trace_id":null,"prev_hash":""}`,
		`{"schema_version":1,"event":"request_started","id":"01JC0000000000000000000003","ts":"2026-10-29T01:00:02Z","instance":"i","principal":{"key_id":"ik_a","team":"platform"},"request":{"ingress":"anthropic","model_requested":"m","stream":false},"trace_id":null,"prev_hash":""}`,
		`not json`,
	}
	for range 2 { // idempotent re-ingest
		if _, err := ix.Replay(strings.NewReader(strings.Join(lines, "\n") + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	got, next, err := ix.AuditEvents(AuditFilter{Limit: 10})
	if err != nil || next != "" || len(got) != 2 {
		t.Fatalf("events = %+v next=%q err=%v", got, next, err)
	}
	if got[0].Kind != KindPolicyDenied || got[0].DenyCode != "key_rate_limited" || got[1].Kind != KindOK {
		t.Fatalf("kinds = %+v", got)
	}
	detail, ok, err := ix.AuditEvent("01JC0000000000000000000002")
	if err != nil || !ok || !strings.Contains(string(detail.Record), `"key_rate_limited"`) {
		t.Fatalf("detail = %+v ok=%v err=%v", detail, ok, err)
	}
	if _, ok, _ := ix.AuditEvent("01JC0000000000000000000009"); ok {
		t.Fatal("unknown id must not be found")
	}
	sum, err := ix.AuditSummary(AuditFilter{})
	if err != nil || sum.ByKind[KindPolicyDenied] != 1 || sum.ByKind[KindOK] != 1 || len(sum.Denials) != 1 || sum.CoverageStartMs == 0 {
		t.Fatalf("summary = %+v err=%v", sum, err)
	}
}

func TestIngestAuditFiltersAndSessions(t *testing.T) {
	ix := openIndex(t)
	mk := func(id, kind string, mutate func(*audit.Record)) {
		r := rec(id, "request_completed", 200, mutate)
		if err := ix.IngestAudit(r, nil); err != nil {
			t.Fatal(err)
		}
	}
	mk("01JC0000000000000000000011", "", func(r *audit.Record) {
		r.Client = &audit.ClientRef{RequestClass: "main", SessionRef: "s1:" + strings.Repeat("a", 32)}
		r.Request.Routing = &audit.RoutingRef{Masked: true, Categories: []string{"email", "BAD CATEGORY"}}
	})
	mk("01JC0000000000000000000012", "", func(r *audit.Record) {
		r.Client = &audit.ClientRef{RequestClass: "subagent", SessionRef: "s1:" + strings.Repeat("a", 32)}
	})
	mk("01JC0000000000000000000013", "", func(r *audit.Record) { r.Principal.KeyID = "ik_b" })
	pii, _, err := ix.AuditEvents(AuditFilter{PII: true, Limit: 10})
	if err != nil || len(pii) != 1 || strings.Join(pii[0].Categories, ",") != "email" {
		t.Fatalf("pii = %+v err=%v", pii, err)
	}
	byCat, _, _ := ix.AuditEvents(AuditFilter{Category: "email", Limit: 10})
	byKey, _, _ := ix.AuditEvents(AuditFilter{KeyID: "ik_b", Limit: 10})
	if len(byCat) != 1 || len(byKey) != 1 {
		t.Fatalf("category=%d key=%d", len(byCat), len(byKey))
	}
	page, next, _ := ix.AuditEvents(AuditFilter{Limit: 2})
	rest, _, _ := ix.AuditEvents(AuditFilter{Limit: 2, Before: next})
	if len(page) != 2 || next == "" || len(rest) != 1 {
		t.Fatalf("paging: %d next=%q rest=%d", len(page), next, len(rest))
	}
	sessions, _, err := ix.AuditSessions(AuditFilter{Limit: 10})
	if err != nil || len(sessions) != 1 || sessions[0].Requests != 2 || sessions[0].Subagents != 1 || sessions[0].PII != 1 {
		t.Fatalf("sessions = %+v err=%v", sessions, err)
	}
	inSession, _, err := ix.AuditSessionEvents(sessions[0].SessionRef, AuditFilter{Limit: 10})
	if err != nil || len(inSession) != 2 || inSession[0].ID > inSession[1].ID {
		t.Fatalf("session events = %+v err=%v", inSession, err)
	}
}
```

Run: `go test ./internal/analytics/ -run 'KindOf|AuditObservable|Waterfall|Replay|IngestAudit' > /home/atomoh/.tmp/inferplane-audit-console-20261029/t8.log 2>&1; grep -E '^(ok|FAIL|---|.*undefined)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t8.log`
Expected: FAIL `undefined: KindOf`.

- [ ] **Step 2: Implement projection, kind and waterfall**

`internal/analytics/auditevents.go`:

```go
package analytics

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
)

// Console kinds (ADR-057). Derived here, never written to the audit chain.
const (
	KindOK              = "ok"
	KindPolicyDenied    = "policy_denied"
	KindPrivacyBlocked  = "privacy_blocked"
	KindPrivacyMasked   = "privacy_masked"
	KindPrivacyInternal = "privacy_internal"
	KindUpstreamError   = "upstream_error"
	KindAutoModeBlocked = "auto_mode_blocked"
)

var AuditKinds = []string{KindOK, KindPolicyDenied, KindPrivacyBlocked, KindPrivacyMasked, KindPrivacyInternal, KindUpstreamError, KindAutoModeBlocked}

var (
	privacyBlockCodes = map[string]bool{"sensitive_blocked": true, "mask_failed": true, "masking_failed": true}
	policyDenyCodes   = map[string]bool{"model_forbidden": true, "no_safe_route": true, "budget_target_changed": true, "budget_authority_unavailable": true}
	closedCode        = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// AuditObservable selects terminal request records: completed records and
// started records that carry an outcome (every gateway denial).
func AuditObservable(r audit.Record) bool {
	switch r.Event {
	case "request_completed":
		return true
	case "request_started":
		return r.Outcome != nil
	}
	return false
}

func denyCodeOf(r audit.Record) string {
	if r.Outcome == nil {
		return ""
	}
	if r.Outcome.Error != nil {
		return *r.Outcome.Error
	}
	if r.Outcome.Status >= 400 && r.Request.Routing != nil {
		return r.Request.Routing.Reason
	}
	return ""
}

func boundedCode(s string) string {
	if s == "" || closedCode.MatchString(s) {
		return s
	}
	return "other"
}

// KindOf applies the spec's first-match-wins table.
func KindOf(r audit.Record) string {
	if r.AutoMode != nil && r.AutoMode.Verdict == "block" {
		return KindAutoModeBlocked
	}
	status, partial := 0, false
	if r.Outcome != nil {
		status, partial = r.Outcome.Status, r.Outcome.Partial
	}
	privacy, masked := "", r.Request.PIIMasked
	if rt := r.Request.Routing; rt != nil {
		privacy, masked = rt.Privacy, masked || rt.Masked
	}
	if status >= 400 {
		code := denyCodeOf(r)
		switch {
		case privacy == "block" || privacyBlockCodes[code]:
			return KindPrivacyBlocked
		case audit.KnownDenyReason(code) || policyDenyCodes[code]:
			return KindPolicyDenied
		default:
			return KindUpstreamError
		}
	}
	switch {
	case partial:
		return KindUpstreamError
	case masked:
		return KindPrivacyMasked
	case privacy == "internal_only":
		return KindPrivacyInternal
	}
	return KindOK
}

// AuditEvent is one console row; every string comes from a closed set or the record itself.
type AuditEvent struct {
	ID              string   `json:"id"`
	TS              string   `json:"ts"`
	Event           string   `json:"event"`
	Kind            string   `json:"kind"`
	Team            string   `json:"team"`
	KeyID           string   `json:"key_id"`
	Ingress         string   `json:"ingress"`
	Stream          bool     `json:"stream"`
	Status          int      `json:"status"`
	DenyCode        string   `json:"deny_code,omitempty"`
	RoutingReason   string   `json:"routing_reason,omitempty"`
	Privacy         string   `json:"privacy,omitempty"`
	Masked          bool     `json:"masked,omitempty"`
	WouldRestrict   bool     `json:"would_restrict,omitempty"`
	PII             bool     `json:"pii,omitempty"`
	PolicyRef       string   `json:"policy_ref,omitempty"`
	PlannedBoundary string   `json:"planned_boundary,omitempty"`
	ActualBoundary  string   `json:"actual_boundary,omitempty"`
	RequestClass    string   `json:"request_class,omitempty"`
	SessionRef      string   `json:"session_ref,omitempty"`
	TraceID         string   `json:"trace_id,omitempty"`
	ModelRequested  string   `json:"model_requested"`
	ModelActual     string   `json:"model_actual,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Fallback        bool     `json:"fallback,omitempty"`
	AttemptCount    int      `json:"attempt_count"`
	AutoVerdict     string   `json:"auto_verdict,omitempty"`
	AutoStage       string   `json:"auto_stage,omitempty"`
	AutoSignature   string   `json:"auto_signature,omitempty"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CostMicros      int64    `json:"cost_micros"`
	TotalMs         int64    `json:"total_ms"`
	TTFTMs          int64    `json:"ttft_ms,omitempty"`
	HasBody         bool     `json:"has_body,omitempty"`
	Categories      []string `json:"categories,omitempty"`
}

// AuditEventRow adds the storage-only columns.
type AuditEventRow struct {
	AuditEvent
	TSMs       int64
	RecordJSON string
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// ProjectAuditEvent turns one terminal record into a console row. line is
// the exact audit line when known (Mode A); otherwise the canonical bytes.
func ProjectAuditEvent(r audit.Record, line []byte) (AuditEventRow, bool) {
	if !AuditObservable(r) || r.ID == "" {
		return AuditEventRow{}, false
	}
	row := AuditEventRow{AuditEvent: AuditEvent{ID: r.ID, TS: r.TS, Event: r.Event, Kind: KindOf(r),
		Team: r.Principal.Team, KeyID: r.Principal.KeyID, Ingress: r.Request.Ingress, Stream: r.Request.Stream,
		Masked: r.Request.PIIMasked, AttemptCount: len(r.Attempts), HasBody: r.BodyRef != nil && *r.BodyRef != ""}}
	if t, err := time.Parse(time.RFC3339Nano, r.TS); err == nil {
		row.TSMs = t.UnixMilli()
	}
	if r.Outcome != nil {
		row.Status, row.Fallback = r.Outcome.Status, r.Outcome.FallbackUsed
	}
	if row.Status >= 400 {
		row.DenyCode = boundedCode(denyCodeOf(r))
	}
	var cats []string
	rt := r.Request.Routing
	if rt != nil {
		row.RoutingReason, row.Privacy = boundedCode(rt.Reason), boundedCode(rt.Privacy)
		row.Masked = row.Masked || rt.Masked
		row.PlannedBoundary, row.ActualBoundary = rt.PlannedBoundary, rt.ActualBoundary
		if len(rt.Policies) > 0 {
			p := rt.Policies[0]
			row.PolicyRef = fmt.Sprintf("%s#%d·%s", p.Name, p.Generation, p.Rule)
		}
		for _, c := range rt.Classifiers {
			row.WouldRestrict = row.WouldRestrict || c.WouldRestrict
		}
		for _, c := range rt.Categories {
			if closedCode.MatchString(c) && !slices.Contains(cats, c) {
				cats = append(cats, c)
			}
		}
	}
	slices.Sort(cats)
	row.Categories = cats
	row.PII = len(cats) > 0 || row.Masked || row.Privacy != "" || row.WouldRestrict
	if c := r.Client; c != nil {
		row.RequestClass, row.SessionRef = c.RequestClass, c.SessionRef
	}
	if r.TraceID != nil {
		row.TraceID = *r.TraceID
	}
	var reqModel, actModel, actProvider string
	if rt != nil {
		reqModel, actModel, actProvider = rt.RequestedModel, rt.ActualModel, rt.ActualProvider
	}
	row.ModelRequested = firstNonEmpty(reqModel, r.Request.ModelSubstitutedFrom, r.Request.ModelRequested)
	if r.Event == "request_completed" {
		row.ModelActual = firstNonEmpty(actModel, r.Request.ModelRequested)
	}
	row.Provider = firstNonEmpty(r.Request.Provider, actProvider)
	if row.Provider == "" && len(r.Attempts) > 0 {
		row.Provider = r.Attempts[len(r.Attempts)-1].Provider
	}
	row.Fallback = row.Fallback || len(r.Attempts) > 1
	if a := r.AutoMode; a != nil {
		row.AutoVerdict, row.AutoStage, row.AutoSignature = boundedCode(a.Verdict), boundedCode(a.Stage), a.Signature
	}
	if u := r.Usage; u != nil {
		row.InputTokens, row.OutputTokens = u.InputTokens, u.OutputTokens
	}
	if c := r.Cost; c != nil {
		row.CostMicros = c.AmountUSDMicros
	}
	if l := r.Latency; l != nil {
		row.TotalMs, row.TTFTMs = l.TotalMs, l.TTFTMs
	}
	if len(line) > 0 {
		row.RecordJSON = string(bytes.TrimRight(line, "\r\n"))
	} else if b, err := r.Canonical(); err == nil {
		row.RecordJSON = string(b)
	}
	return row, true
}

// WaterfallRow is one bar of the trace timeline.
type WaterfallRow struct {
	Kind     string `json:"kind"` // gateway | attempt | settle | denied
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   string `json:"status,omitempty"`
	HTTP     int    `json:"http,omitempty"`
	StartMs  int64  `json:"start_ms"`
	DurMs    int64  `json:"dur_ms"`
}

type Waterfall struct {
	TotalMs int64          `json:"total_ms"`
	TTFTMs  int64          `json:"ttft_ms,omitempty"`
	Rows    []WaterfallRow `json:"rows"`
}

// WaterfallOf derives the timeline. A record with no latency has no rows:
// the console then says "no timing recorded".
func WaterfallOf(r audit.Record) Waterfall {
	w := Waterfall{Rows: []WaterfallRow{}}
	if r.Latency == nil {
		return w
	}
	w.TotalMs, w.TTFTMs = r.Latency.TotalMs, r.Latency.TTFTMs
	if len(r.Attempts) == 0 {
		if r.Event == "request_started" && r.Outcome != nil {
			w.Rows = append(w.Rows, WaterfallRow{Kind: "denied", Status: boundedCode(denyCodeOf(r)), HTTP: r.Outcome.Status, DurMs: w.TotalMs})
		}
		return w
	}
	if first := r.Attempts[0].StartMs; first > 0 {
		w.Rows = append(w.Rows, WaterfallRow{Kind: "gateway", DurMs: first})
	}
	for _, a := range r.Attempts {
		w.Rows = append(w.Rows, WaterfallRow{Kind: "attempt", Provider: a.Provider, Model: a.Model, Status: a.Status, HTTP: a.HTTP, StartMs: a.StartMs, DurMs: a.DurMs})
	}
	last := r.Attempts[len(r.Attempts)-1]
	if end := last.StartMs + last.DurMs; w.TotalMs > end {
		w.Rows = append(w.Rows, WaterfallRow{Kind: "settle", StartMs: end, DurMs: w.TotalMs - end})
	} else {
		w.TotalMs = end
	}
	return w
}
```

- [ ] **Step 3: Implement the shared query library**

`internal/analytics/auditquery.go`:

```go
package analytics

import (
	"encoding/json"
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

const deniedKinds = `('policy_denied','privacy_blocked')`

type RowScanner interface{ Scan(dest ...any) error }

// QueryFunc runs a query and calls each for every row.
type QueryFunc func(query string, args []any, each func(RowScanner) error) error

// ExecFunc runs one statement.
type ExecFunc func(query string, args ...any) error

type AuditFilter struct {
	Kinds                                                             []string
	Team, KeyID, Model, Provider, Code, Category, Class, Session, TraceID string
	PII, AutoOnly                                                     bool
	SinceMs, UntilMs                                                  int64
	Limit                                                             int
	Before, After                                                     string
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

func (b *sqlb) where(cond string, v any) { b.conds = append(b.conds, strings.Replace(cond, "?", b.arg(v), 1)) }

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
		m := b.arg(f.Model)
		b.conds = append(b.conds, "(e.model_requested = "+m+" OR e.model_actual = "+m+")")
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
	var out []AuditEvent
	err := run(auditSelect+b.clause()+" ORDER BY e.id "+order+" LIMIT "+b.arg(limit+1), b.args, func(s RowScanner) error {
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
	q += " ORDER BY MAX(e.id) DESC LIMIT " + b.arg(f.Limit+1)
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
		" GROUP BY e.deny_code, e.policy_ref, e.team, e.key_id ORDER BY COUNT(*) DESC LIMIT 50", b.args, func(s RowScanner) error {
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
```

Note: `SUM(...)` over an empty set is NULL in both engines; the GROUP BY queries never produce an empty-set row, and the shadow/coverage queries use `COUNT`/`COALESCE`. If a scan of NULL fails in testing, wrap the affected `SUM` in `COALESCE(…, 0)` and ledger the ruling.

- [ ] **Step 4: Implement the SQLite side**

`internal/analytics/auditevents_sqlite.go`:

```go
package analytics

import (
	"database/sql"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
)

type sqlExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func sqliteExec(e sqlExec) ExecFunc {
	return func(q string, args ...any) error { _, err := e.Exec(q, args...); return err }
}

func (ix *Index) run(q string, args []any, each func(RowScanner) error) error {
	rows, err := ix.db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func ingestAudit(e sqlExec, r audit.Record, line []byte) error {
	row, ok := ProjectAuditEvent(r, line)
	if !ok {
		return nil
	}
	return InsertAuditEvent(SQLiteDialect, sqliteExec(e), row)
}

// IngestAudit projects one terminal record into the console table.
func (ix *Index) IngestAudit(r audit.Record, line []byte) error {
	if err := ingestAudit(ix.db, r, line); err != nil {
		return err
	}
	ix.markIngest(time.Now().UTC())
	return nil
}

func (ix *Index) AuditEvents(f AuditFilter) ([]AuditEvent, string, error) {
	return RunAuditEvents(SQLiteDialect, ix.run, f)
}
func (ix *Index) AuditEvent(id string) (AuditEventDetail, bool, error) {
	return RunAuditEvent(SQLiteDialect, ix.run, id)
}
func (ix *Index) AuditSessions(f AuditFilter) ([]AuditSession, string, error) {
	return RunAuditSessions(SQLiteDialect, ix.run, f)
}
func (ix *Index) AuditSessionEvents(ref string, f AuditFilter) ([]AuditEvent, string, error) {
	return RunAuditSessionEvents(SQLiteDialect, ix.run, ref, f)
}
func (ix *Index) AuditSummary(f AuditFilter) (AuditSummary, error) {
	return RunAuditSummary(SQLiteDialect, ix.run, f)
}

var _ AuditEventQuerier = (*Index)(nil)
```

In `index.go` `OpenSQLite`, after `migrateEventsColumns(db)` succeeds:

```go
	if _, err := db.Exec(AuditEventsDDL); err != nil {
		db.Close()
		return nil, err
	}
```

In `Replay`, inside the complete-line branch, after the existing `Observable` ingest block and still inside `if json.Unmarshal(...) == nil`, add (restructure the `if` so the unmarshal result is reused):

```go
			if AuditObservable(rec) {
				if e := ingestAudit(tx, rec, []byte(strings.TrimRight(line, "\r\n"))); e != nil {
					return n, e
				}
			}
```

(`*sql.Tx` satisfies `sqlExec`.)

In `sink.go` carry the line so the exact bytes are stored:

```go
type sinkItem struct {
	rec  audit.Record
	line []byte
}
```

change the channel to `chan sinkItem`, `NewSink` to `make(chan sinkItem, sinkQueueCap)`, the worker to

```go
	for it := range s.ch {
		if Observable(it.rec) {
			_ = s.ix.Ingest(it.rec) // best-effort; a derived-index error never propagates
		}
		if AuditObservable(it.rec) {
			_ = s.ix.IngestAudit(it.rec, it.line)
		}
	}
```

and `Write` to skip only records that are neither `Observable` nor `AuditObservable`, enqueueing `sinkItem{rec: rec, line: append([]byte(nil), line...)}`.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/analytics/... -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t8.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t8.log`
Expected: `ok` for `internal/analytics` and `internal/analytics/pgstore` (pgstore tests skip without a DSN).

- [ ] **Step 6: Commit**

```bash
git add internal/analytics
git commit -s -m "feat(analytics): project terminal audit records for the console

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Admin audit API and capabilities

**Files:**
- Create: `internal/server/auditconsoleapi/auditconsoleapi.go`, `internal/server/auditconsoleapi/auditconsoleapi_test.go`
- Modify: `internal/server/configapi/capabilities.go` (`AuditConsole`, `TraceLinkTemplate`)
- Modify: `internal/server/server.go` (mount routes when `analyticsQ` implements `analytics.AuditEventQuerier`)
- Modify: `cmd/mayu/gateway.go` (capabilities)
- Test: `internal/server/audit_console_mount_test.go`

**Interfaces:**
- Consumes: `analytics.AuditEventQuerier`, `analytics.AuditFilter`, `analytics.AuditKinds` (Task 8).
- Produces: `func auditconsoleapi.EventsHandler(q analytics.AuditEventQuerier) http.Handler`, `EventHandler`, `SessionsHandler`, `SessionHandler`, `SummaryHandler`; `func auditconsoleapi.ParseFilter(v url.Values, now time.Time, maxLimit int) (analytics.AuditFilter, string)`; `configapi.Capabilities.AuditConsole bool` (`json:"audit_console"`), `configapi.Capabilities.TraceLinkTemplate string` (`json:"trace_link_template,omitempty"`).

- [ ] **Step 1: Write the failing API tests**

`internal/server/auditconsoleapi/auditconsoleapi_test.go`:

```go
package auditconsoleapi

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/analytics"
)

type fakeQ struct{ last analytics.AuditFilter }

func (f *fakeQ) AuditEvents(x analytics.AuditFilter) ([]analytics.AuditEvent, string, error) {
	f.last = x
	return []analytics.AuditEvent{{ID: "01JC0000000000000000000001", Kind: "ok"}}, "01JC0000000000000000000001", nil
}
func (f *fakeQ) AuditEvent(id string) (analytics.AuditEventDetail, bool, error) {
	return analytics.AuditEventDetail{Event: analytics.AuditEvent{ID: id}, Record: json.RawMessage(`{}`)}, id == "01JC0000000000000000000001", nil
}
func (f *fakeQ) AuditSessions(x analytics.AuditFilter) ([]analytics.AuditSession, string, error) {
	f.last = x
	return []analytics.AuditSession{}, "", nil
}
func (f *fakeQ) AuditSessionEvents(ref string, x analytics.AuditFilter) ([]analytics.AuditEvent, string, error) {
	f.last = x
	return []analytics.AuditEvent{}, "", nil
}
func (f *fakeQ) AuditSummary(x analytics.AuditFilter) (analytics.AuditSummary, error) {
	f.last = x
	return analytics.AuditSummary{ByKind: map[string]int64{}}, nil
}

var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func TestParseFilterNormalizesOffsets(t *testing.T) {
	f, code := ParseFilter(url.Values{"since": {"2026-10-29T09:00:00+09:00"}, "until": {"2026-10-29T10:00:00Z"}}, now, 200)
	if code != "" || f.SinceMs != time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("since=%d code=%q", f.SinceMs, code)
	}
}

func TestParseFilterDefaultsAndErrors(t *testing.T) {
	f, code := ParseFilter(url.Values{}, now, 200)
	if code != "" || f.Limit != 100 || f.UntilMs != now.UnixMilli() || f.SinceMs != now.Add(-24*time.Hour).UnixMilli() {
		t.Fatalf("defaults = %+v %q", f, code)
	}
	for q, want := range map[string]string{
		"kind=ok,bogus":                     "invalid_kind",
		"limit=201":                         "invalid_limit",
		"limit=0":                           "invalid_limit",
		"before=not-a-ulid":                 "invalid_cursor",
		"since=yesterday":                   "invalid_range",
		"since=2026-10-29T12:00:00Z&until=2026-10-29T11:00:00Z": "invalid_range",
		"since=2025-01-01T00:00:00Z":        "invalid_range",
		"session=raw-id":                    "invalid_filter",
		"trace_id=XYZ":                      "invalid_filter",
		"code=Not%20Closed":                 "invalid_filter",
	} {
		v, _ := url.ParseQuery(q)
		if _, code := ParseFilter(v, now, 200); code != want {
			t.Fatalf("%s -> %q, want %q", q, code, want)
		}
	}
}

func TestEventsHandler(t *testing.T) {
	q := &fakeQ{}
	rec := httptest.NewRecorder()
	EventsHandler(q).ServeHTTP(rec, httptest.NewRequest("GET", "/admin/audit/events?kind=policy_denied,ok&pii=true", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"next":"01JC0000000000000000000001"`) || len(q.last.Kinds) != 2 || !q.last.PII {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), q.last)
	}
	bad := httptest.NewRecorder()
	EventsHandler(q).ServeHTTP(bad, httptest.NewRequest("GET", "/admin/audit/events?limit=999", nil))
	if bad.Code != 400 || strings.TrimSpace(bad.Body.String()) != `{"error":"invalid_limit"}` {
		t.Fatalf("bad = %d %s", bad.Code, bad.Body.String())
	}
}

func TestEventHandlerNotFound(t *testing.T) {
	q := &fakeQ{}
	req := httptest.NewRequest("GET", "/admin/audit/events/01JC0000000000000000000009", nil)
	req.SetPathValue("id", "01JC0000000000000000000009")
	rec := httptest.NewRecorder()
	EventHandler(q).ServeHTTP(rec, req)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/admin/audit/events/x", nil)
	req.SetPathValue("id", "x")
	rec = httptest.NewRecorder()
	EventHandler(q).ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("bad id = %d", rec.Code)
	}
}

func TestSessionHandlerLimit(t *testing.T) {
	q := &fakeQ{}
	ref := "s1:" + strings.Repeat("a", 32)
	req := httptest.NewRequest("GET", "/admin/audit/sessions/"+ref+"?limit=500", nil)
	req.SetPathValue("ref", ref)
	rec := httptest.NewRecorder()
	SessionHandler(q).ServeHTTP(rec, req)
	if rec.Code != 200 || q.last.Limit != 500 {
		t.Fatalf("%d limit=%d", rec.Code, q.last.Limit)
	}
}
```

Run: `go test ./internal/server/auditconsoleapi/ > /home/atomoh/.tmp/inferplane-audit-console-20261029/t9.log 2>&1; grep -E '^(ok|FAIL|---|.*(undefined|no non-test))' /home/atomoh/.tmp/inferplane-audit-console-20261029/t9.log`
Expected: FAIL (package has no implementation).

- [ ] **Step 2: Implement the handlers**

`internal/server/auditconsoleapi/auditconsoleapi.go`:

```go
// Package auditconsoleapi serves the read-only audit console API (ADR-057):
// /admin/audit/events, /events/{id}, /sessions, /sessions/{ref}, /summary.
// Mounted behind AdminAuth + requireAdmin. Every error is a closed code.
package auditconsoleapi

import (
	"encoding/json"
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

func fail(w http.ResponseWriter, status int, code string) { writeJSON(w, status, map[string]string{"error": code}) }

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

func EventsHandler(q analytics.AuditEventQuerier) http.Handler {
	return get(func(w http.ResponseWriter, r *http.Request) {
		f, code := ParseFilter(r.URL.Query(), time.Now(), listLimit)
		if code != "" {
			fail(w, http.StatusBadRequest, code)
			return
		}
		events, next, err := q.AuditEvents(f)
		if err != nil {
			fail(w, http.StatusInternalServerError, "query_failed")
			return
		}
		if events == nil {
			events = []analytics.AuditEvent{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events, "next": next})
	})
}

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
			fail(w, http.StatusInternalServerError, "query_failed")
		case !ok:
			fail(w, http.StatusNotFound, "not_found")
		default:
			writeJSON(w, http.StatusOK, d)
		}
	})
}

func SessionsHandler(q analytics.AuditEventQuerier) http.Handler {
	return get(func(w http.ResponseWriter, r *http.Request) {
		f, code := ParseFilter(r.URL.Query(), time.Now(), listLimit)
		if code != "" {
			fail(w, http.StatusBadRequest, code)
			return
		}
		sessions, next, err := q.AuditSessions(f)
		if err != nil {
			fail(w, http.StatusInternalServerError, "query_failed")
			return
		}
		if sessions == nil {
			sessions = []analytics.AuditSession{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "next": next})
	})
}

func SessionHandler(q analytics.AuditEventQuerier) http.Handler {
	return get(func(w http.ResponseWriter, r *http.Request) {
		ref := r.PathValue("ref")
		if !sessionRE.MatchString(ref) {
			fail(w, http.StatusBadRequest, "invalid_filter")
			return
		}
		f, code := ParseFilter(r.URL.Query(), time.Now(), sessionLimit)
		if code != "" {
			fail(w, http.StatusBadRequest, code)
			return
		}
		events, next, err := q.AuditSessionEvents(ref, f)
		if err != nil {
			fail(w, http.StatusInternalServerError, "query_failed")
			return
		}
		if events == nil {
			events = []analytics.AuditEvent{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events, "next": next})
	})
}

func SummaryHandler(q analytics.AuditEventQuerier) http.Handler {
	return get(func(w http.ResponseWriter, r *http.Request) {
		f, code := ParseFilter(r.URL.Query(), time.Now(), listLimit)
		if code != "" {
			fail(w, http.StatusBadRequest, code)
			return
		}
		s, err := q.AuditSummary(f)
		if err != nil {
			fail(w, http.StatusInternalServerError, "query_failed")
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
}
```

Run Step 1's command. Expected: `ok`.

- [ ] **Step 3: Write the failing mount test**

`internal/server/audit_console_mount_test.go`: build `AdminMux` exactly as an existing admin-mux test in `internal/server` does (find one with `grep -ln 'AdminMux(' internal/server/*_test.go`), passing an `analytics.Index` opened on a temp file as `analyticsQ`, then assert:
- `GET /admin/audit/events` with the static admin token → `200`;
- the same with a team-mapped (non-admin) OIDC identity → `403` (reuse that test file's OIDC fake if it has one; otherwise assert `401` without a token and record the ruling);
- with `analyticsQ == nil` → `404`.

- [ ] **Step 4: Mount and advertise**

In `internal/server/server.go`, inside the existing `if analyticsQ != nil {` block after the `/admin/logs` mount:

```go
		// Audit console (ADR-057): read-only, full-admin only, mounted only when
		// the analytics store also implements the console projection.
		if aq, ok := analyticsQ.(analytics.AuditEventQuerier); ok {
			for pattern, h := range map[string]http.Handler{
				"GET /admin/audit/events":          auditconsoleapi.EventsHandler(aq),
				"GET /admin/audit/events/{id}":     auditconsoleapi.EventHandler(aq),
				"GET /admin/audit/sessions":        auditconsoleapi.SessionsHandler(aq),
				"GET /admin/audit/sessions/{ref}":  auditconsoleapi.SessionHandler(aq),
				"GET /admin/audit/summary":         auditconsoleapi.SummaryHandler(aq),
			} {
				mux.Handle(pattern, AdminAuth(adminTokens, verifier, mapping, denied, requireAdmin(h, emit)))
			}
		}
```

In `internal/server/configapi/capabilities.go` add to `Capabilities`:

```go
	// AuditConsole: /admin/audit/* is mounted (ADR-057).
	AuditConsole bool `json:"audit_console"`
	// TraceLinkTemplate: optional otel.trace_link_template for console links.
	TraceLinkTemplate string `json:"trace_link_template,omitempty"`
```

In `cmd/mayu/gateway.go` capabilities closure, before `return configapi.Capabilities{`:

```go
		var aq any
		if pgstoreQ != nil {
			aq = pgstoreQ
		} else if analyticsIdx != nil {
			aq = analyticsIdx
		}
		_, auditConsole := aq.(analytics.AuditEventQuerier)
		traceLink := ""
		if cfg.OTel != nil {
			traceLink = cfg.OTel.TraceLinkTemplate
		}
```

and set `AuditConsole: auditConsole, TraceLinkTemplate: traceLink,` in the literal.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/server/... ./cmd/mayu/ -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t9.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t9.log | grep -v '^ok' ; echo done`
Expected: only `done` (no FAIL lines).

- [ ] **Step 6: Commit**

```bash
git add internal/server/auditconsoleapi internal/server/configapi/capabilities.go internal/server/server.go internal/server/audit_console_mount_test.go cmd/mayu/gateway.go
git commit -s -m "feat(admin): read-only audit console API (ADR-057)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Console Audit view

**Files:**
- Create: `internal/server/adminui/static/audit.js`, `internal/server/adminui/testdata/audit_console.cjs`
- Modify: `internal/server/adminui/static/index.html` (nav item, `view-audit` section, script tag), `internal/server/adminui/static/app.js` (`VIEWS`, refreshers), `internal/server/adminui/static/i18n.js` (keys), `internal/server/adminui/static/style.css`, `internal/server/adminui/adminui.go` (`audit.js` content type), `internal/server/adminui/adminui_test.go`, `internal/server/adminui/testdata/browser_qa.py` (serve `audit.js`)
- Create: `internal/server/adminui/audit_console_test.go`

**Interfaces:**
- Consumes: the API JSON shapes of Task 9; `caps.audit_console`, `caps.trace_link_template`; existing `$`, `api`, `msg`, `capOn`, `usd`, `whoamiIsAdmin`, `renderBodyDrawer`, `DISABLED`.
- Produces: global `refreshAuditView()` (called by `showView`), pure helpers `auditKindClass`, `auditWindow`, `auditQuery`, `auditResolveKey`, `auditKeyLabel`, `auditTraceLink`, `auditDuration`, `auditBar`, `auditScope`, `auditSparkPoints`.

- [ ] **Step 1: Write the failing Go checks**

`internal/server/adminui/audit_console_test.go`:

```go
package adminui

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestAuditViewWired(t *testing.T) {
	_, html := get(t, "/index.html")
	_, js := get(t, "/audit.js")
	_, app := get(t, "/app.js")
	for _, want := range []string{`data-view="audit"`, `id="view-audit"`, `data-cap="audit_console"`, `<script src="audit.js"></script>`} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %s", want)
		}
	}
	if !strings.Contains(app, `audit: "nav.audit"`) || !strings.Contains(app, "audit: refreshAuditView") {
		t.Error("app.js VIEWS/refreshers missing audit")
	}
	for _, banned := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "localStorage", "sessionStorage", "eval("} {
		if strings.Contains(js, banned) {
			t.Errorf("audit.js must not use %s", banned)
		}
	}
	for _, want := range []string{"/admin/audit/events", "/admin/audit/sessions", "/admin/audit/summary", "/admin/audit/verify", "/admin/bodies/"} {
		if !strings.Contains(js, want) {
			t.Errorf("audit.js does not call %s", want)
		}
	}
}

func TestAuditI18nKeysHaveFourLanguages(t *testing.T) {
	js, _ := os.ReadFile("static/audit.js")
	html, _ := os.ReadFile("static/index.html")
	dict, _ := os.ReadFile("static/i18n.js")
	keys := map[string]bool{"nav.audit": true}
	for _, m := range regexp.MustCompile(`msg\("((?:audit|nav)\.[A-Za-z0-9_.]+)"\)`).FindAllStringSubmatch(string(js), -1) {
		keys[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`data-i18n(?:-placeholder)?="(audit\.[A-Za-z0-9_.]+)"`).FindAllStringSubmatch(string(html), -1) {
		keys[m[1]] = true
	}
	for _, kind := range []string{"ok", "policy_denied", "privacy_blocked", "privacy_masked", "privacy_internal", "upstream_error", "auto_mode_blocked"} {
		keys["audit.kind."+kind] = true
	}
	for _, scope := range []string{"team", "key", "user", "route"} {
		keys["audit.scope."+scope] = true
	}
	for _, wf := range []string{"gateway", "settle", "denied"} {
		keys["audit.wf."+wf] = true
	}
	for k := range keys {
		line := regexp.MustCompile(`(?m)^\s*"` + regexp.QuoteMeta(k) + `": \{.*\},?$`).FindString(string(dict))
		for _, lang := range []string{"en:", "ko:", "zh:", "ja:"} {
			if !strings.Contains(line, lang) {
				t.Errorf("i18n key %s missing %s", k, lang)
			}
		}
	}
}

func TestAuditConsoleNodeChecks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("audit console behavior checks require Node (standard library only)")
	}
	out, err := exec.Command(node, "testdata/audit_console.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("audit_console.cjs failed: %v\n%s", err, out)
	}
}
```

Also add `"/audit.js"` to the path list in `TestAssetsAreDataFreeAndTokenSafe` and the asset map in `TestServesAssets` (`"/audit.js": "text/javascript"`).

`internal/server/adminui/testdata/audit_console.cjs`:

```js
// Pure helper checks for audit.js (no DOM). Run: node testdata/audit_console.cjs
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const ctx = vm.createContext({ navigator: { language: "en" }, document: { getElementById: () => null, documentElement: {} }, URLSearchParams, console });
for (const f of ["i18n.js", "audit.js"]) vm.runInContext(fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8"), ctx, { filename: f });
const run = (src) => vm.runInContext(src, ctx);
assert.equal(run('auditKindClass("policy_denied")'), "err");
assert.equal(run('auditKindClass("auto_mode_blocked")'), "est");
assert.equal(run('auditKindClass("upstream_error")'), "warn");
assert.equal(run('msg("audit.kind.upstream_error")'), "Error");
const w = JSON.parse(run('JSON.stringify(auditWindow("7d", Date.UTC(2026, 9, 29, 12)))'));
assert.equal(w.since, "2026-10-22T12:00:00.000Z");
assert.equal(run('auditQuery({ a: "", b: "x", c: null, d: 2 })'), "?b=x&d=2");
run('auditState.keys = new Map([["ik_1", { key_id: "ik_1", metadata: { name: "laptop" } }], ["ik_2", { key_id: "ik_2", metadata: { name: "ci" } }], ["ik_3", { key_id: "ik_3", metadata: { name: "ci" } }]])');
assert.equal(run('JSON.stringify(auditResolveKey("laptop"))'), '{"keyId":"ik_1","ambiguous":false}');
assert.equal(run('JSON.stringify(auditResolveKey("ci"))'), '{"keyId":"","ambiguous":true}');   // ambiguous names never pick one
assert.equal(run('JSON.stringify(auditResolveKey("ik_9"))'), '{"keyId":"ik_9","ambiguous":false}');
assert.equal(run('auditKeyLabel("ik_1")'), "laptop");
assert.equal(run('auditKeyLabel("ik_9")'), "ik_9");
const trace = "4bf92f3577b34da6a3ce929d0e0e4736";
assert.equal(run(`auditTraceLink("https://g.example/x?t={trace_id}", "${trace}")`), `https://g.example/x?t=${trace}`);
assert.equal(run(`auditTraceLink("https://g.example/x?t={trace_id}", "not-hex")`), "");
assert.equal(run(`auditTraceLink("javascript:alert({trace_id})", "${trace}")`), "");
assert.equal(run('auditDuration(0)'), "—");
assert.equal(run('auditDuration(412)'), "412ms");
assert.equal(run('auditDuration(6810)'), "6.81s");
assert.equal(run('JSON.stringify(auditBar({ start_ms: 0, dur_ms: 0 }, 0))'), '{"left":0,"width":100}'); // zero total: full-width neutral bar, no NaN
assert.equal(run('JSON.stringify(auditBar({ start_ms: 250, dur_ms: 500 }, 1000))'), '{"left":25,"width":50}');
assert.equal(run('auditScope("team_budget_exceeded")'), "team");
assert.equal(run('auditScope("key_rate_limited")'), "key");
assert.equal(run('auditScope("sensitive_blocked")'), "route");
assert.equal(run('auditSparkPoints([0, 0, 0], 120, 32).length'), 3);
console.log("audit_console.cjs: ok");
```

Run: `go test ./internal/server/adminui/ -run 'Audit|ServesAssets|DataFree' > /home/atomoh/.tmp/inferplane-audit-console-20261029/t10.log 2>&1; grep -E '^(ok|FAIL|---|    )' /home/atomoh/.tmp/inferplane-audit-console-20261029/t10.log`
Expected: FAIL (`/audit.js` 404, missing markup).

- [ ] **Step 2: Serve the asset and add the markup**

`adminui.go`: add `"audit.js": "text/javascript; charset=utf-8",` to `contentTypes`. `testdata/browser_qa.py`: add `'audit.js'` to the static allow-list tuple and to the content-type map.

`index.html`:
- after the Logs nav button: `<button class="nav" data-view="audit">◉&ensp;<span data-i18n="nav.audit">Audit</span></button>`
- after `</section>` of `view-logs`, insert:

```html
      <section id="view-audit" class="view" hidden>
        <div class="card affordance" data-cap="audit_console">
          <div class="microlabel" data-i18n="audit.enableTitle">audit console</div>
          <p class="hint" data-i18n="audit.enableHint">Turn on the analytics index (a file audit sink enables it) to review requests, sessions, PII handling and denials from the audit chain.</p>
        </div>
        <p id="audit-admin-only" class="hint" hidden data-i18n="audit.adminOnly">Only full administrators can open the audit console.</p>
        <div id="audit-content" hidden>
          <div class="audit-head">
            <div class="audit-filters">
              <label for="audit-range"><span data-i18n="audit.range">Period</span>
                <select id="audit-range"><option value="24h" data-i18n="audit.range24h">Last 24 hours</option><option value="7d" data-i18n="audit.range7d">Last 7 days</option><option value="30d" data-i18n="audit.range30d">Last 30 days</option></select></label>
              <label for="audit-team"><span data-i18n="audit.team">Team</span><input id="audit-team" type="search" autocomplete="off"></label>
              <label for="audit-key"><span data-i18n="audit.key">Key</span><input id="audit-key" type="search" autocomplete="off" data-i18n-placeholder="audit.keyPlaceholder" placeholder="key_id or metadata name"></label>
            </div>
            <div class="audit-tools">
              <span id="audit-freshness" class="hint"></span>
              <button id="audit-verify" type="button" class="ghost" data-i18n="audit.verifyChain">Verify chain</button>
              <span id="audit-verify-result" class="hint"></span>
            </div>
          </div>
          <p id="audit-coverage" class="hint"></p>
          <p id="audit-filter-note" class="hint" hidden></p>
          <div class="audit-cards" id="audit-cards"></div>
          <div class="audit-tabs" role="tablist" aria-label="Audit">
            <button type="button" role="tab" class="audit-tab" id="audit-tab-traces" data-audit-tab="traces" aria-selected="true" aria-controls="audit-panel-traces" data-i18n="audit.tabTraces">Traces</button>
            <button type="button" role="tab" class="audit-tab" id="audit-tab-sessions" data-audit-tab="sessions" aria-selected="false" aria-controls="audit-panel-sessions" data-i18n="audit.tabSessions">Sessions</button>
            <button type="button" role="tab" class="audit-tab" id="audit-tab-pii" data-audit-tab="pii" aria-selected="false" aria-controls="audit-panel-pii" data-i18n="audit.tabPII">PII detections</button>
            <button type="button" role="tab" class="audit-tab" id="audit-tab-denials" data-audit-tab="denials" aria-selected="false" aria-controls="audit-panel-denials" data-i18n="audit.tabDenials">Denials</button>
          </div>
          <div id="audit-panel-traces" role="tabpanel" aria-labelledby="audit-tab-traces">
            <div id="audit-kinds" class="audit-kinds"></div>
            <div class="audit-split">
              <div class="card audit-list"><div class="table-wrap"><table class="data" id="audit-trace-table"><thead><tr>
                <th data-i18n="audit.time">time</th><th data-i18n="audit.kindCol">kind</th><th data-i18n="audit.teamKey">team · key</th><th data-i18n="audit.class">class</th>
                <th data-i18n="audit.model">model (requested → actual)</th><th class="num" data-i18n="audit.tokens">tokens in/out</th><th class="num" data-i18n="audit.cost">cost</th><th class="num" data-i18n="audit.latency">latency</th>
              </tr></thead><tbody></tbody></table></div>
                <button id="audit-more" type="button" class="ghost" hidden data-i18n="audit.loadMore">Load more</button></div>
              <div class="card audit-detail" id="audit-detail" aria-live="polite"></div>
            </div>
          </div>
          <div id="audit-panel-sessions" role="tabpanel" aria-labelledby="audit-tab-sessions" hidden>
            <div class="audit-split">
              <div class="card audit-list"><div class="table-wrap"><table class="data" id="audit-session-table"><thead><tr>
                <th data-i18n="audit.sessionCol">session</th><th data-i18n="audit.teamKey">team · key</th><th data-i18n="audit.period">period</th>
                <th class="num" data-i18n="audit.requests">requests</th><th class="num" data-i18n="audit.cost">cost</th><th data-i18n="audit.flags">flags</th>
              </tr></thead><tbody></tbody></table></div></div>
              <div class="card" id="audit-session-detail"></div>
            </div>
            <p class="hint" data-i18n="audit.sessionHint">Sessions group requests by a key-scoped digest of X-Claude-Code-Session-Id. The raw ID is never stored.</p>
          </div>
          <div id="audit-panel-pii" role="tabpanel" aria-labelledby="audit-tab-pii" hidden>
            <div class="audit-two">
              <div class="card"><p class="microlabel" data-i18n="audit.piiCats">Detected categories</p><div id="audit-pii-cats" class="audit-bars"></div><p class="hint" data-i18n="audit.piiCatsHint">One request can match several categories, so the total can exceed the request count.</p></div>
              <div class="card"><p class="microlabel" data-i18n="audit.piiActions">Handling</p><div id="audit-pii-actions" class="audit-bars"></div><p class="hint" data-i18n="audit.piiShadowHint">Shadow policies do not block; they record “would restrict”.</p></div>
            </div>
            <div class="card audit-list"><div class="table-wrap"><table class="data" id="audit-pii-table"><thead><tr>
              <th data-i18n="audit.time">time</th><th data-i18n="audit.teamKey">team · key</th><th data-i18n="audit.category">category</th><th data-i18n="audit.handling">handling</th>
              <th data-i18n="audit.policy">policy · rule</th><th data-i18n="audit.boundary">boundary (planned → actual)</th>
            </tr></thead><tbody></tbody></table></div></div>
          </div>
          <div id="audit-panel-denials" role="tabpanel" aria-labelledby="audit-tab-denials" hidden>
            <div class="audit-two">
              <div class="card audit-list"><h2 data-i18n="audit.policyDenials">Policy denials</h2><p class="hint" data-i18n="audit.policyDenialsHint">From audit deny codes · certain</p>
                <div class="table-wrap"><table class="data" id="audit-deny-table"><thead><tr>
                  <th data-i18n="audit.code">code</th><th data-i18n="audit.policy">policy · rule</th><th data-i18n="audit.scopeCol">scope</th><th class="num" data-i18n="audit.count">count</th><th data-i18n="audit.last">last</th>
                </tr></thead><tbody></tbody></table></div></div>
              <div class="card audit-list"><h2><span data-i18n="audit.autoTitle">Auto mode blocks</span> <span class="audit-pill est" data-i18n="audit.estimate">Estimate</span></h2>
                <p class="hint" data-i18n="audit.autoHint">Claude Code classifier verdicts read from &lt;block&gt; responses.</p>
                <p id="audit-auto-warning" class="hint audit-warn" hidden></p>
                <div class="table-wrap"><table class="data" id="audit-auto-table"><thead><tr>
                  <th data-i18n="audit.time">time</th><th data-i18n="audit.sessionKey">session · key</th><th data-i18n="audit.stage">stage</th><th data-i18n="audit.verdict">verdict</th><th data-i18n="audit.model">model</th>
                </tr></thead><tbody></tbody></table></div>
                <p class="hint audit-callout" data-i18n="audit.autoCallout">The gateway sees only the classifier's decision. Which tool call was blocked is visible only when body capture is on.</p></div>
            </div>
          </div>
        </div>
      </section>
```
- before `<script src="evaluation.js"></script>` add `<script src="audit.js"></script>`.

`app.js`: add `audit: "nav.audit",` to `VIEWS` after `logs`, and `audit: refreshAuditView,` to the `refreshers` object in `showView`.

- [ ] **Step 3: Write audit.js**

`internal/server/adminui/static/audit.js`:

```js
"use strict";

// Audit view (ADR-057): read-only evidence from the audit chain through
// /admin/audit/*. textContent-only rendering; nothing is kept in browser
// storage (ADR-001). Solid badges are certain; dashed badges are estimates.
const AUDIT_KINDS = ["ok", "policy_denied", "privacy_blocked", "privacy_masked", "privacy_internal", "upstream_error", "auto_mode_blocked"];
const AUDIT_KIND_CLASS = { ok: "ok", policy_denied: "err", privacy_blocked: "err", privacy_masked: "warn", privacy_internal: "warn", upstream_error: "warn", auto_mode_blocked: "est" };
const AUDIT_RANGES = { "24h": 86400000, "7d": 7 * 86400000, "30d": 30 * 86400000 };
const AUDIT_TRACE_ID = /^[0-9a-f]{32}$/;
let auditState = { tab: "traces", kinds: new Set(), events: [], next: "", selected: "", keys: new Map(), generation: 0, summary: null };

function auditKindClass(kind) { return AUDIT_KIND_CLASS[kind] || "warn"; }
function auditWindow(range, nowMs) {
  const span = AUDIT_RANGES[range] || AUDIT_RANGES["24h"];
  return { since: new Date(nowMs - span).toISOString(), until: new Date(nowMs).toISOString() };
}
function auditQuery(params) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== "" && v != null) q.set(k, String(v));
  const s = q.toString();
  return s ? "?" + s : "";
}
// Keys have no name field; metadata.name is the operator's label. An
// ambiguous name never silently filters to one key.
function auditResolveKey(text) {
  if (!text) return { keyId: "", ambiguous: false };
  if (auditState.keys.has(text)) return { keyId: text, ambiguous: false };
  const hits = [...auditState.keys.values()].filter(k => k.metadata && k.metadata.name === text);
  if (hits.length === 1) return { keyId: hits[0].key_id, ambiguous: false };
  if (hits.length > 1) return { keyId: "", ambiguous: true };
  return { keyId: text, ambiguous: false };
}
function auditKeyLabel(keyId) {
  const k = auditState.keys.get(keyId);
  return k && k.metadata && k.metadata.name ? k.metadata.name : (keyId || "—");
}
function auditTraceLink(template, traceId) {
  if (!template || !AUDIT_TRACE_ID.test(traceId || "")) return "";
  const url = template.split("{trace_id}").join(traceId);
  return /^https?:\/\/[^/]/.test(url) ? url : "";
}
function auditDuration(ms) {
  if (!ms) return "—";
  return ms >= 1000 ? (ms / 1000).toFixed(ms >= 10000 ? 1 : 2) + "s" : ms + "ms";
}
function auditBar(row, total) {
  if (!total) return { left: 0, width: 100 };
  const left = Math.min(100, Math.max(0, row.start_ms / total * 100));
  const width = Math.max(0.4, Math.min(100 - left, row.dur_ms / total * 100));
  return { left: Math.round(left * 100) / 100, width: Math.round(width * 100) / 100 };
}
function auditScope(code) {
  if (/^team_/.test(code)) return "team";
  if (/^key_/.test(code)) return "key";
  if (/^user_/.test(code)) return "user";
  return "route";
}
function auditSparkPoints(series, w, h) {
  const top = Math.max(1, ...series), step = series.length > 1 ? w / (series.length - 1) : 0;
  return series.map((v, i) => [Math.round(i * step * 10) / 10, Math.round((h - 2 - v / top * (h - 6)) * 10) / 10]);
}

function auditEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}
function auditPill(text, cls) { return auditEl("span", "audit-pill " + cls, text); }
function auditKindPill(kind) { return auditPill(msg("audit.kind." + kind), auditKindClass(kind)); }
function auditCell(tr, content, cls) {
  const td = auditEl("td", cls || "");
  if (content instanceof Node) td.appendChild(content); else td.textContent = content == null ? "" : String(content);
  tr.appendChild(td);
  return td;
}
function auditTime(ts) { return ts ? ts.replace("T", " ").slice(0, 19) : ""; }

function auditFilters() {
  const w = auditWindow($("audit-range").value, Date.now());
  const key = auditResolveKey($("audit-key").value.trim());
  const note = $("audit-filter-note");
  note.hidden = !key.ambiguous;
  note.textContent = key.ambiguous ? msg("audit.keyAmbiguous") : "";
  return { since: w.since, until: w.until, team: $("audit-team").value.trim(), key_id: key.keyId };
}

async function refreshAuditView() {
  const gen = ++auditState.generation;
  const on = capOn("audit_console");
  $("audit-admin-only").hidden = !on || whoamiIsAdmin;
  $("audit-content").hidden = !on || !whoamiIsAdmin;
  if (!on || !whoamiIsAdmin) return;
  const keys = await api("GET", "/admin/keys");
  auditState.keys = new Map(((keys && keys.data) || []).map(k => [k.key_id, k]));
  const f = auditFilters();
  const [summary, health] = await Promise.all([
    api("GET", "/admin/audit/summary" + auditQuery(f)),
    api("GET", "/admin/analytics/health", null, true),
  ]);
  if (gen !== auditState.generation) return;
  auditState.summary = summary;
  renderAuditHeader(summary, health);
  renderAuditCards(summary);
  renderAuditKinds();
  await auditShowTab(auditState.tab);
}

function renderAuditHeader(summary, health) {
  const fresh = health && health !== DISABLED && health.last_ingest_ts ? health.last_ingest_ts : "";
  $("audit-freshness").textContent = fresh ? msg("audit.freshness") + " " + auditTime(fresh) : msg("audit.lagHint");
  $("audit-coverage").textContent = summary.coverage_start_ms
    ? msg("audit.coverage") + " " + auditTime(new Date(summary.coverage_start_ms).toISOString())
    : msg("audit.coverageEmpty");
}

function renderAuditCards(s) {
  const box = $("audit-cards");
  box.textContent = "";
  const total = Object.values(s.by_kind || {}).reduce((a, b) => a + b, 0);
  const denials = (s.by_kind.policy_denied || 0) + (s.by_kind.privacy_blocked || 0);
  const pii = (s.pii.masked || 0) + (s.pii.internal || 0) + (s.pii.blocked || 0) + (s.pii.shadow || 0);
  const auto = s.auto_mode || { block: 0, allow: 0, unknown: 0 };
  const cards = [
    ["audit.cardRequests", total, msg("audit.kind.ok") + " " + (s.by_kind.ok || 0), "requests", "", false],
    ["audit.cardDenials", denials, msg("audit.policyDenialsHint"), "denials", "alert", false],
    ["audit.cardPII", pii, msg("audit.piiMasked") + " " + (s.pii.masked || 0) + " · " + msg("audit.piiInternal") + " " + (s.pii.internal || 0), "pii", "warn", false],
    ["audit.cardAuto", auto.block, msg("audit.verdicts") + " " + (auto.block + auto.allow + auto.unknown), "auto_blocked", "", true],
  ];
  for (const [label, value, sub, series, cls, estimate] of cards) {
    const card = auditEl("div", "card audit-stat " + cls);
    const k = auditEl("div", "audit-stat-k", msg(label));
    if (estimate) k.appendChild(auditPill(msg("audit.estimate"), "est"));
    card.append(k, auditEl("div", "audit-stat-v", String(value)), auditEl("div", "audit-stat-s", sub));
    card.appendChild(auditSpark((s.buckets || []).map(b => b[series] || 0)));
    box.appendChild(card);
  }
}

function auditSpark(series) {
  const ns = "http://www.w3.org/2000/svg", w = 120, h = 32;
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 " + w + " " + h);
  svg.setAttribute("class", "audit-spark");
  svg.setAttribute("aria-hidden", "true");
  if (!series.length) return svg;
  const pts = auditSparkPoints(series, w, h);
  const line = document.createElementNS(ns, "polyline");
  line.setAttribute("points", pts.map(p => p.join(",")).join(" "));
  svg.appendChild(line);
  return svg;
}

function renderAuditKinds() {
  const bar = $("audit-kinds");
  bar.textContent = "";
  const all = auditEl("button", "audit-chip", msg("audit.all"));
  all.type = "button";
  all.setAttribute("aria-pressed", String(auditState.kinds.size === 0));
  all.addEventListener("click", () => { auditState.kinds.clear(); renderAuditKinds(); auditLoadEvents(false); });
  bar.appendChild(all);
  for (const kind of AUDIT_KINDS) {
    const b = auditEl("button", "audit-chip", msg("audit.kind." + kind));
    b.type = "button";
    b.setAttribute("aria-pressed", String(auditState.kinds.has(kind)));
    b.addEventListener("click", () => {
      if (auditState.kinds.has(kind)) auditState.kinds.delete(kind); else auditState.kinds.add(kind);
      renderAuditKinds();
      auditLoadEvents(false);
    });
    bar.appendChild(b);
  }
}

async function auditShowTab(tab) {
  auditState.tab = tab;
  for (const t of ["traces", "sessions", "pii", "denials"]) {
    $("audit-tab-" + t).setAttribute("aria-selected", String(t === tab));
    $("audit-panel-" + t).hidden = t !== tab;
  }
  if (tab === "traces") return auditLoadEvents(false);
  if (tab === "sessions") return auditLoadSessions();
  if (tab === "pii") return auditLoadPII();
  return auditLoadDenials();
}

async function auditLoadEvents(append) {
  const gen = auditState.generation;
  const f = auditFilters();
  const out = await api("GET", "/admin/audit/events" + auditQuery({ ...f, kind: [...auditState.kinds].join(","), limit: 100, before: append ? auditState.next : "" }));
  if (gen !== auditState.generation) return;
  auditState.events = append ? auditState.events.concat(out.events) : out.events;
  auditState.next = out.next || "";
  renderAuditTraces();
}

function renderAuditTraces() {
  const tbody = $("audit-trace-table").querySelector("tbody");
  tbody.textContent = "";
  $("audit-more").hidden = !auditState.next;
  if (!auditState.events.length) {
    const tr = auditEl("tr", "static");
    const td = auditCell(tr, msg("audit.empty"), "hint");
    td.colSpan = 8;
    tbody.appendChild(tr);
    renderAuditDetailEmpty();
    return;
  }
  for (const e of auditState.events) {
    const tr = auditEl("tr", e.id === auditState.selected ? "sel" : "");
    tr.tabIndex = 0;
    auditCell(tr, auditTime(e.ts), "mono nowrap");
    const kind = auditEl("span");
    kind.appendChild(auditKindPill(e.kind));
    if (e.auto_verdict === "allow") kind.appendChild(auditPill(msg("audit.autoAllow"), "est"));
    auditCell(tr, kind);
    const who = auditEl("span", "", e.team);
    who.appendChild(auditEl("span", "sub", auditKeyLabel(e.key_id)));
    auditCell(tr, who);
    auditCell(tr, e.request_class ? auditPill(e.request_class, "cls") : "—");
    const model = auditEl("span", "", e.model_requested);
    if (e.model_actual && e.model_actual !== e.model_requested) model.appendChild(auditEl("span", "sub", "→ " + e.model_actual));
    if (!e.model_actual && e.event === "request_started") model.appendChild(auditEl("span", "sub", msg("audit.deniedBefore")));
    auditCell(tr, model);
    auditCell(tr, e.input_tokens + " / " + e.output_tokens, "num");
    auditCell(tr, e.cost_micros ? usd(e.cost_micros) : "—", "num");
    auditCell(tr, auditDuration(e.total_ms), "num");
    const pick = () => { auditState.selected = e.id; renderAuditTraces(); auditLoadDetail(e.id); };
    tr.addEventListener("click", pick);
    tr.addEventListener("keydown", ev => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); pick(); } });
    tbody.appendChild(tr);
  }
  if (!auditState.selected) renderAuditDetailEmpty();
}

function renderAuditDetailEmpty() {
  const d = $("audit-detail");
  d.textContent = "";
  d.appendChild(auditEl("p", "hint", msg("audit.selectRow")));
}

async function auditLoadDetail(id) {
  const d = $("audit-detail");
  d.textContent = msg("common.loading");
  try {
    renderAuditDetail(await api("GET", "/admin/audit/events/" + encodeURIComponent(id)));
  } catch (err) {
    d.textContent = String(err.message || err);
  }
}

function auditKV(dl, label, value) {
  dl.appendChild(auditEl("dt", "", msg(label)));
  const dd = auditEl("dd");
  if (value instanceof Node) dd.appendChild(value); else dd.textContent = value;
  dl.appendChild(dd);
}

function renderAuditDetail(detail) {
  const e = detail.event, rec = detail.record || {}, d = $("audit-detail");
  d.textContent = "";
  d.appendChild(auditEl("p", "microlabel", msg("audit.detailTitle")));
  d.appendChild(auditEl("h2", "", e.model_requested + (e.model_actual && e.model_actual !== e.model_requested ? " → " + e.model_actual : "")));
  d.appendChild(auditEl("p", "mono hint", msg("audit.record") + " " + e.id));
  d.appendChild(renderAuditWaterfall(detail.waterfall || { rows: [] }));

  const actor = auditEl("dl", "audit-kv");
  const key = auditEl("span", "", auditKeyLabel(e.key_id) + " · " + e.key_id + " ");
  key.appendChild(auditPill(msg("audit.fromAudit"), "ev"));
  auditKV(actor, "audit.keyRow", key);
  const team = auditEl("span", "", e.team + " ");
  team.appendChild(auditPill(msg("audit.fromAudit"), "ev"));
  auditKV(actor, "audit.team", team);
  const info = auditState.keys.get(e.key_id);
  const owner = auditEl("span", "", info ? (info.owner || "—") + " " : msg("audit.notInStore") + " ");
  if (info) owner.appendChild(auditPill(msg("audit.fromStore"), "reg"));
  auditKV(actor, "audit.owner", owner);
  const ident = rec.principal && rec.principal.identity ? JSON.stringify(rec.principal.identity) : "";
  auditKV(actor, "audit.identity", ident || msg("audit.identityOff"));
  d.appendChild(auditSection(msg("audit.actor"), actor));

  const facts = auditEl("dl", "audit-kv");
  auditKV(facts, "audit.session", e.session_ref || msg("audit.noSession"));
  auditKV(facts, "audit.provider", e.provider || "—");
  auditKV(facts, "audit.tokens", e.input_tokens + " / " + e.output_tokens);
  auditKV(facts, "audit.cost", e.cost_micros ? usd(e.cost_micros) : "—");
  d.appendChild(auditSection("", facts));

  if (e.pii) {
    const p = auditEl("dl", "audit-kv");
    auditKV(p, "audit.result", auditKindPill(e.kind));
    const cats = auditEl("span");
    for (const c of e.categories || []) cats.appendChild(auditPill(c, "cat"));
    auditKV(p, "audit.category", cats);
    auditKV(p, "audit.policy", e.policy_ref || "—");
    auditKV(p, "audit.boundary", e.planned_boundary ? e.planned_boundary + " → " + (e.actual_boundary || "—") : msg("audit.deniedBefore"));
    auditKV(p, "audit.maskedRow", msg(e.masked ? "audit.maskedYes" : "audit.maskedNo"));
    d.appendChild(auditSection(msg("audit.privacy"), p));
  }
  if (e.deny_code) {
    const p = auditEl("dl", "audit-kv");
    auditKV(p, "audit.code", e.deny_code + " · HTTP " + e.status);
    auditKV(p, "audit.policy", e.policy_ref || msg("audit.policyNotRecorded"));
    if (e.event === "request_started") auditKV(p, "audit.billing", msg("audit.noBilling"));
    d.appendChild(auditSection(msg("audit.denial"), p));
  }
  if (e.auto_verdict) {
    const p = auditEl("dl", "audit-kv");
    auditKV(p, "audit.stage", e.auto_stage || "—");
    const v = auditEl("span", "", e.auto_verdict + " ");
    v.appendChild(auditPill(msg("audit.estimate") + " · " + e.auto_signature, "est"));
    auditKV(p, "audit.verdict", v);
    d.appendChild(auditSection(msg("audit.autoMode"), p));
  }

  const body = auditEl("div", "audit-callout");
  if (rec.body_ref && capOn("logs_bodies")) {
    const btn = auditEl("button", "primary", msg("audit.openBody"));
    btn.type = "button";
    const box = auditEl("div");
    btn.addEventListener("click", async () => {
      box.textContent = msg("common.loading");
      try {
        const b = await api("GET", "/admin/bodies/" + encodeURIComponent(rec.body_ref), null, true);
        if (b === DISABLED) { box.textContent = msg("body.storeNotEnabled"); return; }
        renderBodyDrawer(box, rec.body_ref, b);
      } catch (err) { box.textContent = String(err.message || err); }
    });
    body.append(btn, auditEl("p", "hint", msg("audit.bodyAudited")), box);
  } else {
    body.appendChild(auditEl("p", "hint", msg("audit.noBody")));
  }
  d.appendChild(auditSection(msg("audit.body"), body));

  const chain = auditEl("dl", "audit-kv");
  const tl = auditTraceLink(caps && caps.trace_link_template, e.trace_id);
  if (tl) {
    const a = auditEl("a", "", msg("audit.openTrace"));
    a.href = tl; a.target = "_blank"; a.rel = "noopener noreferrer";
    const span = auditEl("span", "mono", e.trace_id + " ");
    span.appendChild(a);
    auditKV(chain, "audit.traceId", span);
  } else {
    auditKV(chain, "audit.traceId", e.trace_id || "—");
  }
  auditKV(chain, "audit.prevHash", rec.prev_hash || "—");
  d.appendChild(auditSection(msg("audit.chainPos"), chain));
}

function auditSection(title, child) {
  const s = auditEl("section", "audit-section");
  if (title) s.appendChild(auditEl("p", "microlabel", title));
  s.appendChild(child);
  return s;
}

function renderAuditWaterfall(w) {
  const box = auditEl("div", "audit-wf");
  box.appendChild(auditEl("p", "microlabel", msg("audit.timeline") + " · " + auditDuration(w.total_ms)));
  if (!w.rows || !w.rows.length || !w.total_ms) {
    box.appendChild(auditEl("p", "hint", msg("audit.noTiming")));
    return box;
  }
  for (const r of w.rows) {
    const label = r.kind === "attempt" ? msg("audit.attempt") + " · " + r.provider + (r.http ? " · " + r.http : "")
      : msg("audit.wf." + r.kind);
    const row = auditEl("div", "audit-wf-row");
    row.appendChild(auditEl("span", "lab", label));
    const track = auditEl("div", "audit-wf-track");
    const bar = auditEl("span", "audit-wf-bar " + (r.kind === "denied" || (r.kind === "attempt" && r.status !== "ok") ? "fail" : r.kind === "attempt" ? "ok" : "gate"));
    const g = auditBar(r, w.total_ms);
    bar.style.left = g.left + "%";
    bar.style.width = g.width + "%";
    track.appendChild(bar);
    if (w.ttft_ms && r.kind === "attempt" && r.status === "ok" && w.ttft_ms >= r.start_ms && w.ttft_ms <= r.start_ms + r.dur_ms) {
      const mark = auditEl("span", "audit-wf-mark");
      mark.style.left = auditBar({ start_ms: w.ttft_ms, dur_ms: 0 }, w.total_ms).left + "%";
      mark.title = msg("audit.firstToken") + " " + auditDuration(w.ttft_ms);
      track.appendChild(mark);
    }
    row.append(track, auditEl("span", "ms", auditDuration(r.dur_ms)));
    box.appendChild(row);
  }
  if (w.ttft_ms) box.appendChild(auditEl("p", "hint", msg("audit.firstToken") + " " + auditDuration(w.ttft_ms)));
  return box;
}

async function auditLoadSessions() {
  const out = await api("GET", "/admin/audit/sessions" + auditQuery({ ...auditFilters(), limit: 100 }));
  const tbody = $("audit-session-table").querySelector("tbody");
  tbody.textContent = "";
  $("audit-session-detail").textContent = "";
  if (!out.sessions.length) {
    const tr = auditEl("tr", "static");
    auditCell(tr, msg("audit.sessionsEmpty"), "hint").colSpan = 6;
    tbody.appendChild(tr);
    return;
  }
  for (const s of out.sessions) {
    const tr = auditEl("tr");
    tr.tabIndex = 0;
    auditCell(tr, s.session_ref.slice(0, 11) + "…", "mono nowrap");
    const who = auditEl("span", "", s.team);
    who.appendChild(auditEl("span", "sub", auditKeyLabel(s.key_id)));
    auditCell(tr, who);
    auditCell(tr, auditTime(s.first_ts).slice(11, 16) + "–" + auditTime(s.last_ts).slice(11, 16), "nowrap");
    auditCell(tr, s.requests, "num");
    auditCell(tr, s.cost_micros ? usd(s.cost_micros) : "—", "num");
    const flags = auditEl("span");
    if (s.denials) flags.appendChild(auditPill(msg("audit.denialsShort") + " " + s.denials, "err"));
    if (s.pii) flags.appendChild(auditPill(msg("audit.piiShort") + " " + s.pii, "warn"));
    if (s.auto_blocked) flags.appendChild(auditPill(msg("audit.autoShort") + " " + s.auto_blocked, "est"));
    if (s.subagents) flags.appendChild(auditPill(msg("audit.subagents") + " " + s.subagents, "cls"));
    auditCell(tr, flags);
    const pick = () => auditLoadSession(s);
    tr.addEventListener("click", pick);
    tr.addEventListener("keydown", ev => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); pick(); } });
    tbody.appendChild(tr);
  }
}

async function auditLoadSession(s) {
  const d = $("audit-session-detail");
  d.textContent = msg("common.loading");
  const out = await api("GET", "/admin/audit/sessions/" + encodeURIComponent(s.session_ref) + auditQuery({ limit: 500 }));
  d.textContent = "";
  d.append(auditEl("p", "microlabel", msg("audit.sessionTimeline")), auditEl("h2", "mono", s.session_ref.slice(0, 11) + "…"));
  const list = auditEl("div", "audit-tl");
  for (const e of out.events) {
    const item = auditEl("button", "audit-tl-item" + (e.request_class === "subagent" ? " indent" : ""));
    item.type = "button";
    item.append(auditEl("span", "mono", auditTime(e.ts).slice(11)), auditPill(e.request_class || "—", "cls"),
      auditEl("span", "", e.model_requested), auditKindPill(e.kind));
    item.addEventListener("click", async () => {
      auditState.selected = e.id;
      await auditShowTab("traces");
      auditLoadDetail(e.id);
    });
    list.appendChild(item);
  }
  d.appendChild(list);
  if (out.next) d.appendChild(auditEl("p", "hint", msg("audit.sessionTruncated")));
}

function auditBars(box, rows) {
  box.textContent = "";
  const top = Math.max(1, ...rows.map(r => r[1]));
  for (const [label, count, cls] of rows) {
    const row = auditEl("div", "audit-bar-row");
    const track = auditEl("span", "audit-bar-track");
    const fill = auditEl("span", "audit-bar-fill " + (cls || ""));
    fill.style.width = (count / top * 100).toFixed(1) + "%";
    track.appendChild(fill);
    row.append(auditEl("span", "mono", label), track, auditEl("span", "num", String(count)));
    box.appendChild(row);
  }
}

async function auditLoadPII() {
  const s = auditState.summary;
  auditBars($("audit-pii-cats"), (s.categories || []).map(c => [c.name, c.count, "warn"]));
  auditBars($("audit-pii-actions"), [[msg("audit.piiMasked"), s.pii.masked, "warn"], [msg("audit.piiInternal"), s.pii.internal, "warn"],
    [msg("audit.piiBlocked"), s.pii.blocked, "err"], [msg("audit.piiShadow"), s.pii.shadow, "dim"]]);
  const out = await api("GET", "/admin/audit/events" + auditQuery({ ...auditFilters(), pii: "true", limit: 100 }));
  const tbody = $("audit-pii-table").querySelector("tbody");
  tbody.textContent = "";
  if (!out.events.length) {
    const tr = auditEl("tr", "static");
    auditCell(tr, msg("audit.piiEmpty"), "hint").colSpan = 6;
    tbody.appendChild(tr);
    return;
  }
  for (const e of out.events) {
    const tr = auditEl("tr", "static");
    auditCell(tr, auditTime(e.ts), "mono nowrap");
    const who = auditEl("span", "", e.team);
    who.appendChild(auditEl("span", "sub", auditKeyLabel(e.key_id)));
    auditCell(tr, who);
    const cats = auditEl("span");
    for (const c of e.categories || []) cats.appendChild(auditPill(c, "cat"));
    auditCell(tr, cats);
    auditCell(tr, e.would_restrict && e.kind === "ok" ? auditPill(msg("audit.piiShadow"), "cls") : auditKindPill(e.kind));
    auditCell(tr, e.policy_ref || "—");
    auditCell(tr, e.planned_boundary ? e.planned_boundary + " → " + (e.actual_boundary || "—") : msg("audit.deniedBefore"));
    tbody.appendChild(tr);
  }
}

async function auditLoadDenials() {
  const s = auditState.summary;
  const tbody = $("audit-deny-table").querySelector("tbody");
  tbody.textContent = "";
  if (!s.denials.length) {
    const tr = auditEl("tr", "static");
    auditCell(tr, msg("audit.denialsEmpty"), "hint").colSpan = 5;
    tbody.appendChild(tr);
  }
  for (const x of s.denials) {
    const tr = auditEl("tr", "static");
    auditCell(tr, x.code, "mono");
    auditCell(tr, x.policy || msg("audit.policyNotRecorded"));
    const scope = auditScope(x.code);
    auditCell(tr, msg("audit.scope." + scope) + " " + (scope === "team" ? x.team : auditKeyLabel(x.key_id)));
    auditCell(tr, x.count, "num");
    auditCell(tr, auditTime(x.last_ts), "mono");
    tbody.appendChild(tr);
  }
  const auto = s.auto_mode;
  const warn = $("audit-auto-warning");
  warn.hidden = !auto.unknown;
  warn.textContent = auto.unknown ? msg("audit.signatureWarn") + " (" + auto.unknown + ")" : "";
  const out = await api("GET", "/admin/audit/events" + auditQuery({ ...auditFilters(), auto: "true", limit: 100 }));
  const ab = $("audit-auto-table").querySelector("tbody");
  ab.textContent = "";
  if (!out.events.length) {
    const tr = auditEl("tr", "static");
    auditCell(tr, msg("audit.autoNotObserved"), "hint").colSpan = 5;
    ab.appendChild(tr);
    return;
  }
  for (const e of out.events) {
    const tr = auditEl("tr", "static");
    auditCell(tr, auditTime(e.ts), "mono nowrap");
    const who = auditEl("span", "mono", e.session_ref ? e.session_ref.slice(0, 11) + "…" : "—");
    who.appendChild(auditEl("span", "sub", auditKeyLabel(e.key_id)));
    auditCell(tr, who);
    auditCell(tr, auditPill(e.auto_stage || "—", "cls"));
    auditCell(tr, auditPill(e.auto_verdict, "est"));
    auditCell(tr, e.model_actual || e.model_requested);
    ab.appendChild(tr);
  }
}

async function auditVerifyChain() {
  const out = $("audit-verify-result");
  out.textContent = msg("common.loading");
  try {
    const r = await api("GET", "/admin/audit/verify");
    const sinks = (r && r.sinks) || [];
    if (!sinks.length) { out.textContent = msg("audit.verifyNone"); return; }
    out.textContent = sinks.map(s => s.ok ? msg("audit.verifyOk") + " · " + (s.records || 0) : msg("audit.verifyBroken") + " " + (s.broken_at || s.reason || "")).join(" · ");
  } catch (err) {
    out.textContent = String(err.message || err);
  }
}

if (typeof window !== "undefined" && document.getElementById("audit-range")) {
  for (const id of ["audit-range", "audit-team", "audit-key"]) $(id).addEventListener("change", () => refreshAuditView().catch(showWorkspaceError));
  for (const t of ["traces", "sessions", "pii", "denials"]) $("audit-tab-" + t).addEventListener("click", () => auditShowTab(t).catch(showWorkspaceError));
  $("audit-more").addEventListener("click", () => auditLoadEvents(true).catch(showWorkspaceError));
  $("audit-verify").addEventListener("click", auditVerifyChain);
}
```

Check the `/admin/audit/verify` response field names against `internal/server/auditapi/auditapi.go` (`SinkResult` JSON tags) and adjust `sinks`, `ok`, `records`, `broken_at`, `reason` to match; record the ruling.

- [ ] **Step 4: Add i18n keys and styles**

Append to `MSG` in `i18n.js` (one line per key, all four languages):

```js
  "nav.audit": { en: "Audit", ko: "감사", zh: "审计", ja: "監査" },
  "audit.enableTitle": { en: "audit console", ko: "감사 콘솔", zh: "审计控制台", ja: "監査コンソール" },
  "audit.enableHint": { en: "Turn on the analytics index (a file audit sink enables it) to review requests, sessions, PII handling and denials from the audit chain.", ko: "analytics 인덱스를 켜면(file 감사 sink가 있으면 켜짐) 감사 체인에서 요청, 세션, PII 처리, 거부를 볼 수 있습니다.", zh: "启用分析索引（配置 file 审计 sink 即启用）后，可从审计链查看请求、会话、PII 处理和拒绝记录。", ja: "分析インデックスを有効にすると（file 監査 sink があれば有効）、監査チェーンからリクエスト、セッション、PII の扱い、拒否を確認できます。" },
  "audit.adminOnly": { en: "Only full administrators can open the audit console.", ko: "감사 콘솔은 전체 관리자만 열 수 있습니다.", zh: "只有完全管理员可以打开审计控制台。", ja: "監査コンソールはフル管理者のみ開けます。" },
  "audit.range": { en: "Period", ko: "기간", zh: "时间范围", ja: "期間" },
  "audit.range24h": { en: "Last 24 hours", ko: "최근 24시간", zh: "最近 24 小时", ja: "直近 24 時間" },
  "audit.range7d": { en: "Last 7 days", ko: "최근 7일", zh: "最近 7 天", ja: "直近 7 日" },
  "audit.range30d": { en: "Last 30 days", ko: "최근 30일", zh: "最近 30 天", ja: "直近 30 日" },
  "audit.team": { en: "Team", ko: "팀", zh: "团队", ja: "チーム" },
  "audit.key": { en: "Key", ko: "키", zh: "密钥", ja: "キー" },
  "audit.keyPlaceholder": { en: "key_id or metadata name", ko: "key_id 또는 metadata 이름", zh: "key_id 或 metadata 名称", ja: "key_id または metadata 名" },
  "audit.keyAmbiguous": { en: "Several keys share this name. Enter the key_id instead.", ko: "이 이름을 쓰는 키가 여러 개입니다. key_id를 입력하세요.", zh: "多个密钥使用此名称，请改为输入 key_id。", ja: "この名前のキーが複数あります。key_id を入力してください。" },
  "audit.verifyChain": { en: "Verify chain", ko: "체인 검증", zh: "验证链", ja: "チェーンを検証" },
  "audit.verifyOk": { en: "Chain verified · records", ko: "체인 검증 통과 · 기록", zh: "链验证通过 · 记录", ja: "チェーン検証 OK · 件数" },
  "audit.verifyBroken": { en: "Chain broken at record", ko: "체인이 끊긴 기록:", zh: "链断开于记录", ja: "チェーン破損レコード:" },
  "audit.verifyNone": { en: "No file audit sink to verify. Run mayu audit verify on the exported file.", ko: "검증할 file 감사 sink가 없습니다. 내보낸 파일에 mayu audit verify를 실행하세요.", zh: "没有可验证的 file 审计 sink。请对导出的文件运行 mayu audit verify。", ja: "検証できる file 監査 sink がありません。エクスポートしたファイルで mayu audit verify を実行してください。" },
  "audit.freshness": { en: "Index updated", ko: "인덱스 갱신", zh: "索引更新于", ja: "インデックス更新" },
  "audit.lagHint": { en: "The index can lag the audit file. Restarting mayu replays the file sink; Mode B uses rebuild.", ko: "인덱스는 감사 파일보다 늦을 수 있습니다. mayu를 재시작하면 file sink를 replay하고, Mode B는 rebuild를 씁니다.", zh: "索引可能落后于审计文件。重启 mayu 会重放 file sink；Mode B 使用 rebuild。", ja: "インデックスは監査ファイルより遅れることがあります。mayu を再起動すると file sink を再生し、Mode B は rebuild を使います。" },
  "audit.coverage": { en: "Covers audit records since", ko: "다음 시각 이후의 감사 기록 기준:", zh: "覆盖以下时间之后的审计记录：", ja: "次の時刻以降の監査記録が対象:" },
  "audit.coverageEmpty": { en: "No audit records in the index yet.", ko: "인덱스에 감사 기록이 아직 없습니다.", zh: "索引中还没有审计记录。", ja: "インデックスにまだ監査記録がありません。" },
  "audit.cardRequests": { en: "Requests", ko: "요청", zh: "请求", ja: "リクエスト" },
  "audit.cardDenials": { en: "Policy denials", ko: "정책 거부", zh: "策略拒绝", ja: "ポリシー拒否" },
  "audit.cardPII": { en: "PII detections", ko: "PII 탐지", zh: "PII 检测", ja: "PII 検出" },
  "audit.cardAuto": { en: "Auto mode blocks", ko: "auto mode 차단", zh: "auto mode 拦截", ja: "auto mode ブロック" },
  "audit.estimate": { en: "Estimate", ko: "추정", zh: "估计", ja: "推定" },
  "audit.verdicts": { en: "classifier verdicts", ko: "분류기 판정", zh: "分类器判定", ja: "分類器の判定" },
  "audit.tabTraces": { en: "Traces", ko: "Traces", zh: "追踪", ja: "トレース" },
  "audit.tabSessions": { en: "Sessions", ko: "세션", zh: "会话", ja: "セッション" },
  "audit.tabPII": { en: "PII detections", ko: "PII 탐지", zh: "PII 检测", ja: "PII 検出" },
  "audit.tabDenials": { en: "Denials", ko: "거부 이력", zh: "拒绝记录", ja: "拒否履歴" },
  "audit.all": { en: "All", ko: "전체", zh: "全部", ja: "すべて" },
  "audit.kind.ok": { en: "OK", ko: "정상", zh: "正常", ja: "正常" },
  "audit.kind.policy_denied": { en: "Policy denied", ko: "정책 거부", zh: "策略拒绝", ja: "ポリシー拒否" },
  "audit.kind.privacy_blocked": { en: "PII blocked", ko: "PII 차단", zh: "PII 拦截", ja: "PII ブロック" },
  "audit.kind.privacy_masked": { en: "PII masked", ko: "PII 마스킹", zh: "PII 已脱敏", ja: "PII マスク" },
  "audit.kind.privacy_internal": { en: "PII internal route", ko: "PII 내부 경로", zh: "PII 内部路由", ja: "PII 内部経路" },
  "audit.kind.upstream_error": { en: "Error", ko: "오류", zh: "错误", ja: "エラー" },
  "audit.kind.auto_mode_blocked": { en: "Auto mode blocked", ko: "auto mode 차단", zh: "auto mode 拦截", ja: "auto mode ブロック" },
  "audit.autoAllow": { en: "auto allow", ko: "auto 허용", zh: "auto 允许", ja: "auto 許可" },
  "audit.time": { en: "time", ko: "시각", zh: "时间", ja: "時刻" },
  "audit.kindCol": { en: "kind", ko: "kind", zh: "类型", ja: "種類" },
  "audit.teamKey": { en: "team · key", ko: "팀 · 키", zh: "团队 · 密钥", ja: "チーム · キー" },
  "audit.class": { en: "class", ko: "class", zh: "类别", ja: "クラス" },
  "audit.model": { en: "model (requested → actual)", ko: "모델 (요청 → 실제)", zh: "模型（请求 → 实际）", ja: "モデル（要求 → 実際）" },
  "audit.tokens": { en: "tokens in/out", ko: "토큰 in/out", zh: "令牌 输入/输出", ja: "トークン 入力/出力" },
  "audit.cost": { en: "cost", ko: "비용", zh: "费用", ja: "コスト" },
  "audit.latency": { en: "latency", ko: "지연", zh: "延迟", ja: "レイテンシ" },
  "audit.loadMore": { en: "Load more", ko: "더 보기", zh: "加载更多", ja: "さらに読み込む" },
  "audit.empty": { en: "No requests match these filters. Remove a filter to see more.", ko: "조건에 맞는 요청이 없습니다. 필터를 줄여 보세요.", zh: "没有符合条件的请求。请减少筛选条件。", ja: "条件に合うリクエストがありません。フィルターを減らしてください。" },
  "audit.selectRow": { en: "Select a request to see its trace.", ko: "요청을 선택하면 trace를 볼 수 있습니다.", zh: "选择一个请求查看其追踪。", ja: "リクエストを選ぶとトレースを表示します。" },
  "audit.detailTitle": { en: "trace detail", ko: "trace 상세", zh: "追踪详情", ja: "トレース詳細" },
  "audit.record": { en: "record", ko: "기록", zh: "记录", ja: "レコード" },
  "audit.timeline": { en: "timeline", ko: "timeline", zh: "时间线", ja: "タイムライン" },
  "audit.noTiming": { en: "No timing was recorded for this request (written before attempt timing existed).", ko: "이 요청에는 시간 기록이 없습니다(시도별 시간 기록 전에 쓰인 기록).", zh: "此请求没有计时记录（写于尝试计时功能之前）。", ja: "このリクエストには時間の記録がありません（試行ごとの計測より前の記録）。" },
  "audit.wf.gateway": { en: "Gateway work", ko: "게이트웨이 처리", zh: "网关处理", ja: "ゲートウェイ処理" },
  "audit.wf.settle": { en: "Settle · audit", ko: "정산 · 감사", zh: "结算 · 审计", ja: "精算 · 監査" },
  "audit.wf.denied": { en: "Denied before dispatch", ko: "dispatch 전 거부", zh: "发送前拒绝", ja: "送信前に拒否" },
  "audit.attempt": { en: "Attempt", ko: "시도", zh: "尝试", ja: "試行" },
  "audit.firstToken": { en: "First token", ko: "첫 토큰", zh: "首个令牌", ja: "最初のトークン" },
  "audit.deniedBefore": { en: "denied before dispatch", ko: "dispatch 전 거부", zh: "发送前拒绝", ja: "送信前に拒否" },
  "audit.actor": { en: "Actor", ko: "행위자", zh: "操作者", ja: "実行者" },
  "audit.keyRow": { en: "Key", ko: "키", zh: "密钥", ja: "キー" },
  "audit.owner": { en: "Owner", ko: "소유자", zh: "所有者", ja: "所有者" },
  "audit.identity": { en: "Identity", ko: "identity", zh: "身份", ja: "アイデンティティ" },
  "audit.fromAudit": { en: "audit record", ko: "감사 기록", zh: "审计记录", ja: "監査記録" },
  "audit.fromStore": { en: "per key store", ko: "키 저장소 기준", zh: "以密钥存储为准", ja: "キーストア基準" },
  "audit.notInStore": { en: "not in key store", ko: "키 저장소에 없음", zh: "密钥存储中不存在", ja: "キーストアにない" },
  "audit.identityOff": { en: "verified identity off · not recorded", ko: "verified identity 꺼짐 · 기록 없음", zh: "已验证身份未启用 · 无记录", ja: "verified identity 無効 · 記録なし" },
  "audit.session": { en: "Session", ko: "세션", zh: "会话", ja: "セッション" },
  "audit.noSession": { en: "none (no session header)", ko: "없음 (세션 헤더 없음)", zh: "无（没有会话头）", ja: "なし（セッションヘッダーなし）" },
  "audit.provider": { en: "Provider", ko: "provider", zh: "提供方", ja: "プロバイダー" },
  "audit.privacy": { en: "PII check", ko: "PII 검사", zh: "PII 检查", ja: "PII チェック" },
  "audit.result": { en: "Result", ko: "결과", zh: "结果", ja: "結果" },
  "audit.category": { en: "category", ko: "category", zh: "类别", ja: "カテゴリ" },
  "audit.policy": { en: "policy · rule", ko: "정책 · rule", zh: "策略 · 规则", ja: "ポリシー · ルール" },
  "audit.boundary": { en: "boundary (planned → actual)", ko: "경계 (계획 → 실제)", zh: "边界（计划 → 实际）", ja: "境界（計画 → 実際）" },
  "audit.maskedRow": { en: "Masking", ko: "마스킹", zh: "脱敏", ja: "マスキング" },
  "audit.maskedYes": { en: "completed · reinspected", ko: "완료 · 재검사 통과", zh: "已完成 · 已复检", ja: "完了 · 再検査済み" },
  "audit.maskedNo": { en: "not masked", ko: "안 함", zh: "未脱敏", ja: "マスクなし" },
  "audit.denial": { en: "Denial", ko: "거부", zh: "拒绝", ja: "拒否" },
  "audit.code": { en: "code", ko: "코드", zh: "代码", ja: "コード" },
  "audit.billing": { en: "Provider billing", ko: "provider 과금", zh: "提供方计费", ja: "プロバイダー課金" },
  "audit.noBilling": { en: "none · denied before dispatch", ko: "없음 · dispatch 전 거부", zh: "无 · 发送前拒绝", ja: "なし · 送信前に拒否" },
  "audit.policyNotRecorded": { en: "not recorded for governance denials", ko: "거버넌스 거부에는 기록되지 않음", zh: "治理拒绝不记录策略", ja: "ガバナンス拒否では記録されない" },
  "audit.autoMode": { en: "Auto mode classifier", ko: "auto mode 분류기", zh: "auto mode 分类器", ja: "auto mode 分類器" },
  "audit.stage": { en: "stage", ko: "단계", zh: "阶段", ja: "段階" },
  "audit.verdict": { en: "verdict", ko: "판정", zh: "判定", ja: "判定" },
  "audit.body": { en: "Body", ko: "본문", zh: "正文", ja: "本文" },
  "audit.openBody": { en: "View body", ko: "본문 보기", zh: "查看正文", ja: "本文を表示" },
  "audit.bodyAudited": { en: "Viewing records body_accessed in the audit chain.", ko: "열람하면 body_accessed 감사 기록이 남습니다.", zh: "查看会在审计链中记录 body_accessed。", ja: "表示すると監査チェーンに body_accessed が記録されます。" },
  "audit.noBody": { en: "No body was captured for this request (capture off, denied, or streamed response).", ko: "이 요청은 본문이 캡처되지 않았습니다 (캡처 꺼짐, 거부, 또는 stream 응답).", zh: "此请求未捕获正文（捕获关闭、被拒绝或流式响应）。", ja: "このリクエストの本文は取得されていません（取得無効、拒否、またはストリーム応答）。" },
  "audit.traceId": { en: "trace_id", ko: "trace_id", zh: "trace_id", ja: "trace_id" },
  "audit.openTrace": { en: "Open in tracing backend", ko: "추적 백엔드에서 열기", zh: "在追踪后端中打开", ja: "トレースバックエンドで開く" },
  "audit.prevHash": { en: "prev_hash", ko: "prev_hash", zh: "prev_hash", ja: "prev_hash" },
  "audit.chainPos": { en: "chain position", ko: "체인 위치", zh: "链位置", ja: "チェーン位置" },
  "audit.sessionCol": { en: "session", ko: "세션", zh: "会话", ja: "セッション" },
  "audit.period": { en: "period", ko: "기간", zh: "时间段", ja: "期間" },
  "audit.requests": { en: "requests", ko: "요청", zh: "请求", ja: "リクエスト" },
  "audit.flags": { en: "flags", ko: "주의", zh: "标记", ja: "注意" },
  "audit.sessionHint": { en: "Sessions group requests by a key-scoped digest of X-Claude-Code-Session-Id. The raw ID is never stored.", ko: "세션은 X-Claude-Code-Session-Id의 키 범위 digest로 묶습니다. 원문 ID는 저장하지 않습니다.", zh: "会话按 X-Claude-Code-Session-Id 的密钥范围摘要分组，不存储原始 ID。", ja: "セッションは X-Claude-Code-Session-Id のキー範囲ダイジェストでまとめます。元の ID は保存しません。" },
  "audit.sessionsEmpty": { en: "No sessions in this period. Claude Code sends a session header; other clients may not.", ko: "이 기간에 세션이 없습니다. Claude Code는 세션 헤더를 보내지만 다른 클라이언트는 보내지 않을 수 있습니다.", zh: "此时间段没有会话。Claude Code 会发送会话头，其他客户端可能不会。", ja: "この期間にセッションはありません。Claude Code はセッションヘッダーを送りますが、他のクライアントは送らない場合があります。" },
  "audit.sessionTimeline": { en: "session timeline", ko: "세션 timeline", zh: "会话时间线", ja: "セッションのタイムライン" },
  "audit.sessionTruncated": { en: "Showing the first 500 requests of this session.", ko: "이 세션의 처음 500개 요청만 보여줍니다.", zh: "仅显示此会话的前 500 个请求。", ja: "このセッションの最初の 500 件のみ表示しています。" },
  "audit.subagents": { en: "subagents", ko: "서브에이전트", zh: "子代理", ja: "サブエージェント" },
  "audit.denialsShort": { en: "denials", ko: "거부", zh: "拒绝", ja: "拒否" },
  "audit.piiShort": { en: "PII", ko: "PII", zh: "PII", ja: "PII" },
  "audit.autoShort": { en: "auto mode blocks", ko: "auto mode 차단", zh: "auto mode 拦截", ja: "auto mode ブロック" },
  "audit.sessionKey": { en: "session · key", ko: "세션 · 키", zh: "会话 · 密钥", ja: "セッション · キー" },
  "audit.piiCats": { en: "Detected categories", ko: "탐지된 category", zh: "检测到的类别", ja: "検出されたカテゴリ" },
  "audit.piiCatsHint": { en: "One request can match several categories, so the total can exceed the request count.", ko: "한 요청에서 여러 category가 탐지될 수 있어 합계가 요청 수보다 클 수 있습니다.", zh: "一个请求可能匹配多个类别，因此总数可能超过请求数。", ja: "1 件のリクエストが複数カテゴリに該当するため、合計がリクエスト数を超えることがあります。" },
  "audit.piiActions": { en: "Handling", ko: "처리 결과", zh: "处理方式", ja: "処理" },
  "audit.piiShadowHint": { en: "Shadow policies do not block; they record “would restrict”.", ko: "Shadow 정책은 막지 않고 “would restrict”로만 기록합니다.", zh: "Shadow 策略不拦截，只记录“would restrict”。", ja: "Shadow ポリシーはブロックせず「would restrict」と記録します。" },
  "audit.piiMasked": { en: "Masked then sent", ko: "마스킹 후 전송", zh: "脱敏后发送", ja: "マスクして送信" },
  "audit.piiInternal": { en: "Routed internally", ko: "내부 경로로 변경", zh: "改走内部路由", ja: "内部経路へ変更" },
  "audit.piiBlocked": { en: "Blocked", ko: "차단", zh: "已拦截", ja: "ブロック" },
  "audit.piiShadow": { en: "Shadow · would restrict", ko: "Shadow · would restrict", zh: "Shadow · would restrict", ja: "Shadow · would restrict" },
  "audit.piiEmpty": { en: "No PII detections in this period.", ko: "이 기간에 PII 탐지가 없습니다.", zh: "此时间段没有 PII 检测。", ja: "この期間に PII 検出はありません。" },
  "audit.handling": { en: "handling", ko: "처리", zh: "处理", ja: "処理" },
  "audit.policyDenials": { en: "Policy denials", ko: "정책 거부", zh: "策略拒绝", ja: "ポリシー拒否" },
  "audit.policyDenialsHint": { en: "From audit deny codes · certain", ko: "감사 기록의 거부 코드 기준 · 확정", zh: "基于审计拒绝代码 · 确定", ja: "監査の拒否コードに基づく · 確定" },
  "audit.scopeCol": { en: "scope", ko: "대상", zh: "范围", ja: "対象" },
  "audit.scope.team": { en: "team", ko: "팀", zh: "团队", ja: "チーム" },
  "audit.scope.key": { en: "key", ko: "키", zh: "密钥", ja: "キー" },
  "audit.scope.user": { en: "user", ko: "사용자", zh: "用户", ja: "ユーザー" },
  "audit.scope.route": { en: "route", ko: "라우팅", zh: "路由", ja: "ルーティング" },
  "audit.count": { en: "count", ko: "건수", zh: "次数", ja: "件数" },
  "audit.last": { en: "last", ko: "마지막", zh: "最近", ja: "最終" },
  "audit.denialsEmpty": { en: "No denials in this period.", ko: "이 기간에 거부가 없습니다.", zh: "此时间段没有拒绝。", ja: "この期間に拒否はありません。" },
  "audit.autoTitle": { en: "Auto mode blocks", ko: "auto mode 차단", zh: "auto mode 拦截", ja: "auto mode ブロック" },
  "audit.autoHint": { en: "Claude Code classifier verdicts read from <block> responses.", ko: "Claude Code 분류기 응답의 <block> 판정입니다.", zh: "从 Claude Code 分类器的 <block> 响应读取的判定。", ja: "Claude Code 分類器の <block> 応答から読み取った判定です。" },
  "audit.autoNotObserved": { en: "No classifier calls observed. With a Claude main model, Bedrock often returns the verdict itself, so the gateway sees none.", ko: "분류기 호출이 관측되지 않았습니다. 메인 모델이 Claude면 Bedrock이 판정을 직접 주는 경우가 많아 gateway에는 보이지 않습니다.", zh: "未观察到分类器调用。主模型为 Claude 时，Bedrock 常直接返回判定，网关看不到。", ja: "分類器の呼び出しは観測されていません。メインモデルが Claude の場合、Bedrock が判定を直接返すことが多く、ゲートウェイからは見えません。" },
  "audit.signatureWarn": { en: "Some classifier responses could not be read; the Claude Code format may have changed.", ko: "일부 분류기 응답을 읽지 못했습니다. Claude Code 형식이 바뀌었을 수 있습니다.", zh: "部分分类器响应无法读取，Claude Code 格式可能已更改。", ja: "一部の分類器応答を読み取れませんでした。Claude Code の形式が変わった可能性があります。" },
  "audit.autoCallout": { en: "The gateway sees only the classifier's decision. Which tool call was blocked is visible only when body capture is on.", ko: "gateway는 분류기의 결정만 봅니다. 어떤 tool 호출이 막혔는지는 본문 캡처가 켜진 경우에만 trace 상세에서 확인할 수 있습니다.", zh: "网关只能看到分类器的决定。只有开启正文捕获时，才能看到被拦截的工具调用。", ja: "ゲートウェイが見るのは分類器の決定だけです。どのツール呼び出しがブロックされたかは、本文取得が有効なときだけ確認できます。" },
```

Append to `style.css` (tokens only; both themes inherit):

```css
/* Audit view (ADR-057): solid badges are certain evidence, dashed badges are estimates. */
.audit-head { display:flex; flex-wrap:wrap; justify-content:space-between; gap:12px; margin-bottom:12px; }
.audit-filters, .audit-tools { display:flex; flex-wrap:wrap; gap:8px; align-items:end; }
.audit-filters label { display:flex; flex-direction:column; gap:4px; font-size:10px; font-weight:600; color:var(--dim); }
.audit-cards { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:14px; margin:12px 0 18px; }
.audit-stat-k { font-size:12px; color:var(--dim); display:flex; gap:6px; align-items:center; }
.audit-stat-v { font-size:28px; font-weight:650; font-variant-numeric:tabular-nums; margin-top:8px; }
.audit-stat.alert .audit-stat-v { color:var(--red); } .audit-stat.warn .audit-stat-v { color:var(--amber); }
.audit-stat-s { font-size:11px; color:var(--dim); margin-top:4px; }
.audit-spark { width:100%; height:32px; margin-top:10px; } .audit-spark polyline { fill:none; stroke:var(--accent); stroke-width:1.5; vector-effect:non-scaling-stroke; }
.audit-tabs { display:flex; gap:4px; border-bottom:1px solid var(--line); margin-bottom:16px; overflow-x:auto; }
.audit-tab { border:0; border-bottom:2px solid transparent; border-radius:0; background:transparent; color:var(--dim); padding:10px 14px; white-space:nowrap; }
.audit-tab[aria-selected="true"] { color:var(--accent); border-bottom-color:var(--accent); }
.audit-kinds { display:flex; flex-wrap:wrap; gap:6px; margin-bottom:12px; }
.audit-chip { border-radius:999px; font-size:11px; padding:4px 10px; }
.audit-chip[aria-pressed="true"] { background:var(--accent-soft); border-color:var(--accent); color:var(--accent); }
.audit-split { display:grid; grid-template-columns:minmax(0,1.15fr) minmax(0,1fr); gap:18px; align-items:start; }
.audit-two { display:grid; grid-template-columns:minmax(0,1fr) minmax(0,1fr); gap:18px; align-items:start; margin-bottom:18px; }
.audit-list { padding:0; overflow:hidden; } .audit-list .table-wrap { width:100%; overflow-x:auto; }
#audit-trace-table tbody tr { cursor:pointer; } #audit-trace-table tbody tr.sel { background:var(--accent-soft); }
.audit-pill { display:inline-block; padding:2px 8px; margin-right:4px; border-radius:5px; font-size:10px; font-weight:600; white-space:nowrap; border:1px solid transparent; }
.audit-pill.ok { color:var(--green); background:color-mix(in srgb, var(--green) 12%, transparent); }
.audit-pill.err { color:var(--red); background:color-mix(in srgb, var(--red) 12%, transparent); }
.audit-pill.warn { color:var(--amber); background:color-mix(in srgb, var(--amber) 14%, transparent); }
.audit-pill.est { color:var(--accent); border:1px dashed var(--accent); background:transparent; }
.audit-pill.cls, .audit-pill.cat { color:var(--dim); background:var(--panel-2); border-color:var(--line); font-family:var(--mono); font-weight:500; }
.audit-pill.ev { color:var(--text); background:var(--panel-2); border-color:var(--dim); }
.audit-pill.reg { color:var(--dim); border-color:var(--line); }
.audit-kv { display:grid; grid-template-columns:max-content minmax(0,1fr); gap:6px 14px; font-size:12px; margin:0; }
.audit-kv dt { color:var(--dim); } .audit-kv dd { margin:0; overflow-wrap:anywhere; }
.audit-section { border-top:1px solid var(--line); padding-top:12px; margin-top:12px; }
.audit-wf-row { display:grid; grid-template-columns:minmax(0,150px) minmax(0,1fr) 62px; gap:10px; align-items:center; font-size:11px; margin-top:6px; }
.audit-wf-row .lab { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; } .audit-wf-row .ms { text-align:right; color:var(--dim); }
.audit-wf-track { position:relative; height:14px; background:var(--panel-2); border-radius:3px; }
.audit-wf-bar { position:absolute; top:2px; bottom:2px; min-width:3px; border-radius:2px; background:var(--dim); }
.audit-wf-bar.ok { background:var(--green); } .audit-wf-bar.fail { background:var(--red); }
.audit-wf-mark { position:absolute; top:-3px; bottom:-3px; width:2px; background:var(--amber); }
.audit-callout { border:1px solid var(--line); border-radius:8px; padding:12px 14px; background:var(--panel-2); font-size:12px; }
.audit-warn { color:var(--amber); }
.audit-tl { display:grid; gap:4px; margin-top:10px; }
.audit-tl-item { display:grid; grid-template-columns:62px auto minmax(0,1fr) auto; gap:10px; align-items:center; text-align:left; font-size:12px; border:0; background:transparent; padding:6px 8px; }
.audit-tl-item.indent { padding-left:26px; }
.audit-bar-row { display:grid; grid-template-columns:120px minmax(0,1fr) 40px; gap:10px; align-items:center; font-size:12px; margin-top:8px; }
.audit-bar-track { height:10px; border-radius:3px; background:var(--panel-2); position:relative; }
.audit-bar-fill { position:absolute; inset:0 auto 0 0; border-radius:3px; background:var(--amber); }
.audit-bar-fill.err { background:var(--red); } .audit-bar-fill.dim { background:var(--dim); }
td .sub { display:block; color:var(--dim); font-size:11px; }
@media(max-width:1100px) { .audit-split { grid-template-columns:1fr; } .audit-cards { grid-template-columns:repeat(2,minmax(0,1fr)); } }
@media(max-width:800px) { .audit-two { grid-template-columns:1fr; } }
@media(max-width:480px) { .audit-wf-row { grid-template-columns:96px minmax(0,1fr) 48px; } .audit-tl-item { grid-template-columns:52px minmax(0,1fr) auto; } .audit-tl-item .audit-pill.cls { display:none; } }
```

If `td .sub` already exists in `style.css`, drop that line.

- [ ] **Step 5: Run the checks**

Run: `go test ./internal/server/adminui/ -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t10.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t10.log; node internal/server/adminui/testdata/audit_console.cjs`
Expected: `ok` and `audit_console.cjs: ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/server/adminui
git commit -s -m "feat(console): audit view with traces, sessions, PII and denials

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Console browser QA

**Files:**
- Create: `internal/server/adminui/testdata/audit_qa.py`

**Interfaces:**
- Consumes: `browser_qa.Handler`, `browser_qa.STATIC` (existing harness; Task 10 added `audit.js` to its allow-list).

- [ ] **Step 1: Write the QA script**

`internal/server/adminui/testdata/audit_qa.py`:

```python
"""Exercise the Audit view with synthetic /admin/audit/* responses.
Run: python3 internal/server/adminui/testdata/audit_qa.py --screenshots <local-scratch>/audit-qa
Requires existing Playwright + Chromium. No service, upstream or credentials.
"""
import argparse
import sys
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse

sys.path.insert(0, str(Path(__file__).resolve().parent))
import browser_qa  # noqa: E402
from playwright.sync_api import sync_playwright  # noqa: E402

TRACE = '4bf92f3577b34da6a3ce929d0e0e4736'
SESSION = 's1:' + 'a' * 32
EVENTS = [
    {'id': '01JC0000000000000000000003', 'ts': '2026-10-29T10:44:12Z', 'event': 'request_completed', 'kind': 'ok', 'team': 'engineering',
     'key_id': 'key-engineering-01', 'ingress': 'bedrock', 'stream': False, 'status': 200, 'request_class': 'main', 'session_ref': SESSION,
     'trace_id': TRACE, 'model_requested': 'private-coder', 'model_actual': 'private-coder', 'provider': 'private', 'fallback': True,
     'attempt_count': 2, 'input_tokens': 1200, 'output_tokens': 80, 'cost_micros': 31210, 'total_ms': 6810, 'ttft_ms': 1420},
    {'id': '01JC0000000000000000000002', 'ts': '2026-10-29T10:36:22Z', 'event': 'request_started', 'kind': 'policy_denied', 'team': 'engineering',
     'key_id': 'key-engineering-01', 'ingress': 'anthropic', 'stream': False, 'status': 429, 'deny_code': 'key_rate_limited',
     'request_class': 'subagent', 'session_ref': SESSION, 'model_requested': 'private-coder', 'attempt_count': 0,
     'input_tokens': 0, 'output_tokens': 0, 'cost_micros': 0, 'total_ms': 6},
    {'id': '01JC0000000000000000000001', 'ts': '2026-10-29T10:31:40Z', 'event': 'request_completed', 'kind': 'privacy_masked', 'team': 'platform',
     'key_id': 'key-platform-02', 'ingress': 'anthropic', 'stream': False, 'status': 200, 'masked': True, 'pii': True, 'categories': ['email'],
     'policy_ref': 'org-pii-mask#4·0', 'planned_boundary': 'external', 'actual_boundary': 'external', 'model_requested': 'economy-coder',
     'model_actual': 'economy-coder', 'attempt_count': 1, 'input_tokens': 600, 'output_tokens': 50, 'cost_micros': 2650, 'total_ms': 2980},
]
DETAIL = {'event': EVENTS[0], 'record': {'prev_hash': 'sha256-fixture', 'principal': {'key_id': 'key-engineering-01', 'team': 'engineering'}},
          'waterfall': {'total_ms': 6810, 'ttft_ms': 1420, 'rows': [
              {'kind': 'gateway', 'start_ms': 0, 'dur_ms': 15}, {'kind': 'attempt', 'provider': 'bedrock', 'status': 'upstream_error', 'http': 503, 'start_ms': 15, 'dur_ms': 1225},
              {'kind': 'attempt', 'provider': 'private', 'status': 'ok', 'start_ms': 1240, 'dur_ms': 5560}, {'kind': 'settle', 'start_ms': 6800, 'dur_ms': 10}]}}
LEGACY = {'event': dict(EVENTS[2], total_ms=0), 'record': {}, 'waterfall': {'total_ms': 0, 'rows': []}}
SUMMARY = {'since_ms': 0, 'until_ms': 0, 'by_kind': {'ok': 1, 'policy_denied': 1, 'privacy_masked': 1},
           'categories': [{'name': 'email', 'count': 1}], 'pii': {'masked': 1, 'internal': 0, 'blocked': 0, 'shadow': 0},
           'denials': [{'code': 'key_rate_limited', 'team': 'engineering', 'key_id': 'key-engineering-01', 'count': 1, 'last_ts': '2026-10-29T10:36:22Z'}],
           'auto_mode': {'block': 0, 'allow': 0, 'unknown': 0, 'signatures': []},
           'buckets': [{'start_ms': i, 'requests': i % 3, 'denials': i % 2, 'pii': 0, 'auto_blocked': 0} for i in range(12)],
           'coverage_start_ms': 1793235600000}


class Handler(browser_qa.Handler):
    def do_GET(self):
        path = urlparse(self.path).path
        if path.startswith('/admin/') and self.headers.get('Authorization') == 'Bearer fixture-admin':
            if path == '/admin/capabilities':
                return self.respond(200, {'analytics_index': 'A', 'teams_records': True, 'audit_console': True, 'logs_bodies': False,
                                          'trace_link_template': 'https://grafana.example/explore?traceId={trace_id}'})
            if path == '/admin/audit/summary':
                return self.respond(200, SUMMARY)
            if path == '/admin/audit/events':
                q = self.path
                rows = [e for e in EVENTS if 'pii=true' not in q or e.get('pii')]
                rows = [] if 'auto=true' in q else rows
                return self.respond(200, {'events': rows, 'next': ''})
            if path == '/admin/audit/events/01JC0000000000000000000003':
                return self.respond(200, DETAIL)
            if path == '/admin/audit/events/01JC0000000000000000000001':
                return self.respond(200, LEGACY)
            if path == '/admin/audit/sessions':
                return self.respond(200, {'sessions': [{'session_ref': SESSION, 'team': 'engineering', 'key_id': 'key-engineering-01',
                    'first_ts': '2026-10-29T10:02:11Z', 'last_ts': '2026-10-29T10:44:12Z', 'requests': 2, 'cost_micros': 31210,
                    'denials': 1, 'pii': 0, 'auto_blocked': 0, 'subagents': 1}], 'next': ''})
            if path == '/admin/audit/sessions/' + SESSION:
                return self.respond(200, {'events': list(reversed(EVENTS[:2])), 'next': ''})
            if path == '/admin/analytics/health':
                return self.respond(200, {'mode': 'A', 'last_ingest_ts': '2026-10-29T10:44:13Z'})
        return super().do_GET()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--screenshots', type=Path)
    args = parser.parse_args()
    if args.screenshots:
        args.screenshots.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base = f'http://127.0.0.1:{server.server_port}'
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        for width, scheme in [(1280, 'light'), (390, 'dark')]:
            page = browser.new_page(viewport={'width': width, 'height': 1000}, color_scheme=scheme, locale='en-US')
            errors = []
            page.on('pageerror', lambda e: errors.append(str(e)))
            page.on('console', lambda m: errors.append(m.text) if m.type == 'error' else None)
            page.goto(base + '/admin/ui/')
            page.locator('#token').fill('fixture-admin')
            page.locator('#token-form button').click()
            page.locator('#shell').wait_for(state='visible')
            page.locator('button.nav[data-view="audit"]').click()
            page.wait_for_function("() => document.querySelectorAll('#audit-trace-table tbody tr').length === 3")
            assert page.locator('#audit-cards .audit-stat').count() == 4
            page.locator('#audit-trace-table tbody tr').first.click()
            page.wait_for_function("() => document.querySelectorAll('#audit-detail .audit-wf-row').length === 4")
            assert page.locator('#audit-detail a[href*="traceId=' + TRACE + '"]').count() == 1
            page.locator('#audit-trace-table tbody tr').nth(2).click()
            page.wait_for_function("() => document.getElementById('audit-detail').textContent.includes('No timing was recorded')")
            assert 'NaN' not in page.locator('#audit-detail').inner_text()
            page.locator('#audit-tab-sessions').click()
            page.locator('#audit-session-table tbody tr').first.click()
            page.wait_for_function("() => document.querySelectorAll('#audit-session-detail .audit-tl-item').length === 2")
            assert page.locator('#audit-session-detail .audit-tl-item.indent').count() == 1
            page.locator('#audit-tab-pii').click()
            page.wait_for_function("() => document.querySelectorAll('#audit-pii-table tbody tr').length === 1")
            page.locator('#audit-tab-denials').click()
            page.wait_for_function("() => document.getElementById('audit-auto-table').textContent.includes('No classifier calls observed')")
            overflow = page.evaluate('document.documentElement.scrollWidth - window.innerWidth')
            assert overflow <= 0, f'horizontal overflow {overflow}px at {width}px'
            if args.screenshots:
                page.screenshot(path=str(args.screenshots / f'audit-{width}-{scheme}.png'), full_page=True)
            assert not errors, errors
            page.close()
        browser.close()
    server.shutdown()
    print('audit_qa.py: ok')


if __name__ == '__main__':
    main()
```

- [ ] **Step 2: Run it**

Run: `python3 internal/server/adminui/testdata/audit_qa.py --screenshots /home/atomoh/.tmp/inferplane-audit-console-20261029/audit-qa > /home/atomoh/.tmp/inferplane-audit-console-20261029/t11.log 2>&1; echo "exit=$?"; grep -E 'ok|Error|assert' /home/atomoh/.tmp/inferplane-audit-console-20261029/t11.log`
Expected: `exit=0`, `audit_qa.py: ok`. Also run the existing harness: `python3 internal/server/adminui/testdata/browser_qa.py > …/t11b.log 2>&1; echo "exit=$?"` → `exit=0`. Fix CSS (not the assertions) for any overflow and rerun once.

- [ ] **Step 3: Look at the screenshots once**

Read `/home/atomoh/.tmp/inferplane-audit-console-20261029/audit-qa/audit-1280-light.png` and `audit-390-dark.png` with the Read tool. Fix only visibly broken layout (clipped text, unreadable contrast), rerun Step 2 once.

- [ ] **Step 4: Commit**

```bash
git add internal/server/adminui/testdata/audit_qa.py internal/server/adminui/static/style.css
git commit -s -m "test(console): browser QA for the audit view at desktop and phone widths

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Mode B (Postgres) projection

**Files:**
- Modify: `internal/analytics/pgstore/pgstore.go` (`ensureSchema`, `ingestBatch`, `Rebuild`, querier methods)
- Test: `internal/analytics/pgstore/audit_events_test.go`

**Interfaces:**
- Consumes: `analytics.AuditEventsDDL`, `analytics.InsertAuditEvent`, `analytics.PostgresDialect`, `analytics.Run*`, `analytics.ProjectAuditEvent`, `analytics.AuditObservable` (Task 8).
- Produces: `*pgstore.Store` implements `analytics.AuditEventQuerier` (so Task 9's mount and capability turn on in Mode B).

- [ ] **Step 1: Start a local Postgres (loopback only)**

```bash
docker info > /home/atomoh/.tmp/inferplane-audit-console-20261029/docker.log 2>&1; echo "docker=$?"
docker run -d --rm --name ip-audit-pg -e POSTGRES_PASSWORD=audit-test -p 127.0.0.1:55432:5432 postgres:16 > /home/atomoh/.tmp/inferplane-audit-console-20261029/pg.log 2>&1; echo "run=$?"
export INFERPLANE_TEST_PG_DSN='postgres://postgres:audit-test@127.0.0.1:55432/postgres?sslmode=disable'
for i in $(seq 1 30); do docker exec ip-audit-pg pg_isready -U postgres > /dev/null 2>&1 && break; sleep 1; done; echo ready
```
Expected: `docker=0`, `run=0`, `ready`. If Docker is unavailable, stop and report: Mode B stays unverified and its capability stays off (do not ship untested Mode B queries).

- [ ] **Step 2: Write the failing test**

`internal/analytics/pgstore/audit_events_test.go`:

```go
package pgstore

import (
	"context"
	"testing"

	"github.com/inferplane/inferplane/internal/analytics"
	"github.com/inferplane/inferplane/internal/audit"
)

func TestPgstoreAuditEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	reason := "team_budget_exceeded"
	records := []audit.Record{
		{SchemaVersion: 1, Event: "request_completed", ID: "01JC0000000000000000000101", TS: "2026-10-29T01:00:00Z",
			Principal: audit.PrincipalRef{KeyID: "ik", Team: "t"}, Request: audit.RequestRef{Ingress: "anthropic", ModelRequested: "m"},
			Outcome: &audit.OutcomeRef{Status: 200}, Client: &audit.ClientRef{RequestClass: "main", SessionRef: "s1:" + "b"},
			Latency: &audit.LatencyRef{TotalMs: 10}},
		{SchemaVersion: 1, Event: "request_started", ID: "01JC0000000000000000000102", TS: "2026-10-29T01:00:01Z",
			Principal: audit.PrincipalRef{KeyID: "ik", Team: "t"}, Request: audit.RequestRef{Ingress: "anthropic", ModelRequested: "m"},
			Outcome: &audit.OutcomeRef{Status: 402, Error: &reason}},
	}
	if err := s.ingestBatch(ctx, "holder", 0, records, map[string]int64{}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	events, _, err := s.AuditEvents(analytics.AuditFilter{Limit: 10})
	if err != nil || len(events) != 2 || events[0].Kind != analytics.KindPolicyDenied {
		t.Fatalf("events = %+v err=%v", events, err)
	}
	sum, err := s.AuditSummary(analytics.AuditFilter{SinceMs: 1793232000000, UntilMs: 1793318400000}) // 2026-10-29 UTC
	if err != nil || sum.ByKind[analytics.KindOK] != 1 || len(sum.Buckets) != 12 {
		t.Fatalf("summary = %+v err=%v", sum, err)
	}
	if err := s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if after, _, _ := s.AuditEvents(analytics.AuditFilter{Limit: 10}); len(after) != 0 {
		t.Fatal("rebuild must clear the console table")
	}
}
```

If `ingestBatch` requires a held lease epoch, acquire it the way `aggregator_test.go` does (`tryAcquireLease`) and record the ruling.

Run: `go test ./internal/analytics/pgstore/ -run PgstoreAuditEvents > /home/atomoh/.tmp/inferplane-audit-console-20261029/t12.log 2>&1; grep -E '^(ok|FAIL|---|.*undefined)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t12.log`
Expected: FAIL `s.AuditEvents undefined`.

- [ ] **Step 3: Implement**

In `pgstore.go`:
- In `ensureSchema`, execute `analytics.AuditEventsDDL` in the same locked transaction after the existing schema statements.
- In `ingestBatch`, inside the record loop and before `if !analytics.Observable(r) { continue }`:

```go
		if row, ok := analytics.ProjectAuditEvent(r, nil); ok {
			if err := analytics.InsertAuditEvent(analytics.PostgresDialect, func(q string, args ...any) error {
				_, err := tx.Exec(ctx, q, args...)
				return err
			}, row); err != nil {
				return fmt.Errorf("project audit event %q: %w", r.ID, err)
			}
		}
```
- In `Rebuild`, change the truncate to `TRUNCATE events, rollup_day, checkpoints, audit_events, audit_event_categories`.
- Add the querier:

```go
func (s *Store) run(q string, args []any, each func(analytics.RowScanner) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) AuditEvents(f analytics.AuditFilter) ([]analytics.AuditEvent, string, error) {
	return analytics.RunAuditEvents(analytics.PostgresDialect, s.run, f)
}
func (s *Store) AuditEvent(id string) (analytics.AuditEventDetail, bool, error) {
	return analytics.RunAuditEvent(analytics.PostgresDialect, s.run, id)
}
func (s *Store) AuditSessions(f analytics.AuditFilter) ([]analytics.AuditSession, string, error) {
	return analytics.RunAuditSessions(analytics.PostgresDialect, s.run, f)
}
func (s *Store) AuditSessionEvents(ref string, f analytics.AuditFilter) ([]analytics.AuditEvent, string, error) {
	return analytics.RunAuditSessionEvents(analytics.PostgresDialect, s.run, ref, f)
}
func (s *Store) AuditSummary(f analytics.AuditFilter) (analytics.AuditSummary, error) {
	return analytics.RunAuditSummary(analytics.PostgresDialect, s.run, f)
}

var _ analytics.AuditEventQuerier = (*Store)(nil)
```

Postgres returns `bigint` for `COUNT`/`SUM` and `MIN(ts_ms)`; all scan targets in Task 8 are `int64`, so no change is needed. `(e.ts_ms - $1) / $2` needs bigint parameters; pass `int64` values (Task 8 already does).

- [ ] **Step 4: Run Mode B tests (new and existing) and stop Postgres**

Run: `INFERPLANE_TEST_PG_DSN="$INFERPLANE_TEST_PG_DSN" go test ./internal/analytics/... -race > /home/atomoh/.tmp/inferplane-audit-console-20261029/t12.log 2>&1; grep -E '^(ok|FAIL|---)' /home/atomoh/.tmp/inferplane-audit-console-20261029/t12.log; docker stop ip-audit-pg > /dev/null`
Expected: two `ok` lines; pgstore tests ran (check `grep -c SKIP` of a `-v` run is 0 if in doubt).

- [ ] **Step 5: Commit**

```bash
git add internal/analytics/pgstore
git commit -s -m "feat(pgstore): audit console projection for Mode B

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: Reference docs, module notes, CLAUDE.md and AGENTS.md

**Files:**
- Modify: `docs/reference/api.md`, `docs/reference/api.ko.md` (Audit console endpoints)
- Modify: `docs/reference/data.md`, `docs/reference/data.ko.md` (new audit fields; `audit_events` tables; `audit.client_evidence`; `otel.trace_link_template`)
- Modify: `docs/reference/security.md`, `docs/reference/security.ko.md` (session digest, auto mode estimate, access, metric)
- Modify: `internal/CLAUDE.md` (`audit/`, `analytics/`, `server/` bullets; new `clientctx/` bullet)
- Modify: `CLAUDE.md` (Project Structure gains `clientctx/`; the ADR-044 sentence at lines 243–244 gains the ADR-057 exception)
- Regenerate: `AGENTS.md` via `/co-agent sync-context`
- Modify: `examples/config.json` only if it documents `audit` keys inline (add nothing otherwise)

- [ ] **Step 1: Write the reference sections**

Each English doc gains one section, mirrored in its `.ko.md` with the same headings and facts:
- `api.md` "Audit console (ADR-057)": the five routes, every query parameter with its validation from Task 9 `ParseFilter`, the closed error codes, full-admin requirement, mount condition, and the `audit_console`/`trace_link_template` capability fields.
- `data.md` "Audit console evidence": `attempts[]`, `client`, `auto_mode` field tables (types, closed values, bounds), `latency` now on every terminal record, `fallback_used`/`fallback_chain` now written; the `audit_events` and `audit_event_categories` columns; coverage rule; config keys `audit.client_evidence.{session_ref,auto_mode}` and `otel.trace_link_template`.
- `security.md` "Audit console": the session digest formula and what it links, the `off` switch and that turning it off does not remove existing links, auto mode as estimate with no text stored, access control, and `inferplane_audit_auto_mode_unparsed_total` (no labels).

- [ ] **Step 2: Update module notes and CLAUDE.md**

- `internal/CLAUDE.md`: in `audit/` add one sentence on `evidence.go` (`WithEvidence`/`BeginAttempt`/`Finish`, terminal records only); in `analytics/` add the `audit_events` projection, shared `Dialect`/`Run*` builders and `AuditEventQuerier`; in `server/` add `auditconsoleapi/`; add a `clientctx/` bullet (client headers → bounded evidence; auto mode tap; raw session ID never leaves the package).
- `CLAUDE.md`: in the `internal/` tree add `clientctx/       - Bounded client evidence: request class, key-scoped session digest, auto mode verdict tap (ADR-057)`; change "hash/scoped hints stay out of audit/metrics" to "hash/scoped hints stay out of audit/metrics, except the ADR-057 key-scoped session digest in audit records only".

- [ ] **Step 3: Regenerate AGENTS.md and run doc checks**

Run `/co-agent sync-context` (it rewrites `AGENTS.md` with the new `claude-md-sha`). Then:

```bash
head -1 AGENTS.md | grep -o 'claude-md-sha: [0-9a-f]*'
python3 scripts/check_docs_translations.py > /home/atomoh/.tmp/inferplane-audit-console-20261029/t13.log 2>&1; echo "exit=$?"
git status --short
```
Expected: a new sha, `exit=0`, and only the files listed above changed (revert any unrelated regeneration side effect file by file).

- [ ] **Step 4: Commit**

```bash
git add docs/reference internal/CLAUDE.md CLAUDE.md AGENTS.md
git commit -s -m "docs: document audit console evidence and API (ADR-057)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 14: Live end-to-end on this EC2 host and repository gates

**Files:**
- No product files. Uses `/home/atomoh/.tmp/inferplane-audit-console-20261029/verify/` from Task 1.

- [ ] **Step 1: Repository gates**

```bash
L=/home/atomoh/.tmp/inferplane-audit-console-20261029
go test ./... -race > $L/gate-test.log 2>&1; echo "test=$?"; grep -E '^(FAIL|--- FAIL)' $L/gate-test.log
go vet ./... > $L/gate-vet.log 2>&1; echo "vet=$?"
gofmt -l . > $L/gate-fmt.log; echo "fmt-files=$(wc -l < $L/gate-fmt.log)"
bash tests/run-all.sh > $L/gate-harness.log 2>&1; echo "harness=$?"
python3 scripts/check_docs_translations.py > $L/gate-docs.log 2>&1; echo "docs=$?"
```
Expected: `test=0` with no FAIL lines, `vet=0`, `fmt-files=0`, `harness=0`, `docs=0`.

- [ ] **Step 2: Build and run the test gateway with the branch binary**

```bash
V=$L/verify
CGO_ENABLED=0 go build -trimpath -o $V/mayu ./cmd/mayu > $V/build2.log 2>&1; echo "build=$?"
python3 - "$V" <<'EOF'
import json, pathlib, sys
V = pathlib.Path(sys.argv[1]); c = json.loads((V / 'config.json').read_text())
c['providers']['stub503'] = {'type': 'anthropic', 'base_url': 'http://127.0.0.1:18199', 'api_key_ref': {'env': 'STUB_KEY'}}
c['models']['e2e-fallback'] = {'targets': [{'provider': 'stub503', 'model': 'stub'}, {'provider': 'bedrock-global', 'model': 'global.anthropic.claude-haiku-5-5', 'api': 'invoke_model'}],
                               'context_window': 200000, 'capabilities': ['tools']}
c['pricing'].setdefault('overrides', {}).setdefault('stub503', {})['stub'] = {'input_per_mtok': 1.0, 'output_per_mtok': 1.0}
(V / 'config.json').write_text(json.dumps(c, indent=2))
EOF
python3 -c "
import http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length') or 0)); self.send_response(503); self.send_header('Content-Type','application/json'); self.end_headers(); self.wfile.write(b'{\"type\":\"error\"}')
    def log_message(self,*a): pass
http.server.ThreadingHTTPServer(('127.0.0.1',18199),H).serve_forever()" > $V/stub.log 2>&1 &   # run_in_background
STUB_KEY=stub INFERPLANE_ADMIN_TOKEN=$(cat $V/admin.txt) $V/mayu config validate --config $V/config.json > $V/validate2.log 2>&1; echo "validate=$?"
STUB_KEY=stub INFERPLANE_ADMIN_TOKEN=$(cat $V/admin.txt) $V/mayu serve --config $V/config.json > $V/mayu2.log 2>&1 &   # run_in_background
INFERPLANE_ADMIN_TOKEN=$(cat $V/admin.txt) $V/mayu keys create --team audit-test --models '*' --rpm 1 --store $V/keys.db > $V/key-rpm.out 2>&1; echo "rpmkey=$?"
grep -o 'ik_[A-Za-z0-9_-]*' $V/key-rpm.out | head -n1 > $V/key-rpm.txt
```
If `mayu keys create` has no `--rpm` flag, create the RPM-1 key through `POST /admin/keys` with `{"team":"audit-test","allowed_models":["*"],"rpm":1}` on `127.0.0.1:19191` and record the ruling. If the stub provider's config keys differ from the `anthropic` provider schema, adjust to the schema `mayu config validate` accepts and record the ruling.
Expected: `build=0`, `validate=0`, `rpmkey=0`.

- [ ] **Step 3: Measure boot replay on a copy of the real audit file (Review Focus 5)**

```bash
cp /home/atomoh/inferplane-local/data/audit.jsonl $V/real-audit-copy.jsonl
python3 - "$V" <<'EOF'
import json, pathlib, sys
V = pathlib.Path(sys.argv[1]); c = json.loads((V / 'config.json').read_text())
c['server'] = dict(c['server'], listen='127.0.0.1:18281', admin_listen='127.0.0.1:19291')
c['audit'] = dict(c['audit'], buffer={'path': str(V / 'replay.wal')}, sinks=[{'type': 'file', 'path': str(V / 'real-audit-copy.jsonl')}])
c['key_store'] = {'type': 'sqlite', 'path': str(V / 'replay-keys.db')}
(V / 'replay-config.json').write_text(json.dumps(c, indent=2))
EOF
start=$(date +%s); STUB_KEY=stub INFERPLANE_ADMIN_TOKEN=$(cat $V/admin.txt) timeout 120 $V/mayu serve --config $V/replay-config.json > $V/replay.log 2>&1 &
for i in $(seq 1 120); do curl -fsS -o /dev/null http://127.0.0.1:18281/healthz 2>/dev/null && break; sleep 1; done; echo "replay_seconds=$(( $(date +%s) - start ))"
curl -fsS -H "Authorization: Bearer $(cat $V/admin.txt)" 'http://127.0.0.1:19291/admin/audit/summary?since=2026-10-01T00:00:00Z&until=2026-10-30T00:00:00Z' > $V/replay-summary.json; python3 -c "import json;s=json.load(open('$V/replay-summary.json'));print('kinds',s['by_kind'],'coverage',s['coverage_start_ms'])"
pkill -f "$V/replay-config.json"; grep -c 'analytics replay warning' $V/replay.log
```
Expected: `replay_seconds` < 60, non-empty `kinds`, `0` warnings. The copy is opened by the test gateway only; the live file is never written. Record the measured seconds in the ledger.

- [ ] **Step 4: Generate real traffic**

```bash
cd $V
# fallback: stub503 then Bedrock
curl -fsS -o fb.json -w '%{http_code}\n' http://127.0.0.1:18181/v1/messages -H "x-api-key: $(cat key.txt)" -H 'content-type: application/json' \
  -H 'x-claude-code-request-class: main' -d '{"model":"e2e-fallback","max_tokens":16,"messages":[{"role":"user","content":"Say ok."}]}'
# rate limit: second call within a minute must be 429
for i in 1 2; do curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18181/v1/messages -H "x-api-key: $(cat key-rpm.txt)" -H 'content-type: application/json' \
  -d '{"model":"global.anthropic.claude-haiku-5-5","max_tokens":8,"messages":[{"role":"user","content":"ok"}]}'; done
# real Claude Code sessions (Bedrock mode with a non-Claude main model for local classifier calls, then Anthropic mode)
env -u ANTHROPIC_BASE_URL -u ANTHROPIC_API_KEY CLAUDE_CODE_USE_BEDROCK=1 AWS_REGION=ap-northeast-2 ANTHROPIC_BEDROCK_BASE_URL=http://127.0.0.1:18181 \
  CLAUDE_CODE_SKIP_BEDROCK_AUTH=1 AWS_BEARER_TOKEN_BEDROCK="$(cat key.txt)" ANTHROPIC_MODEL=kimi-k3 \
  claude --permission-mode auto -p 'Run this shell command and report its output: ls /home/atomoh/.tmp' > e2e-b.txt 2>&1; echo "b=$?"
env -u CLAUDE_CODE_USE_BEDROCK -u ANTHROPIC_BEDROCK_BASE_URL ANTHROPIC_BASE_URL=http://127.0.0.1:18181 ANTHROPIC_API_KEY="$(cat key.txt)" \
  ANTHROPIC_MODEL=global.anthropic.claude-sonnet-5 claude --permission-mode auto -p 'Use a subagent to list /home/atomoh/.tmp' > e2e-c.txt 2>&1; echo "c=$?"
```
Expected: fallback `200`; rate limit `200` then `429`; `b` and `c` exit codes recorded.

- [ ] **Step 5: Check the success criteria against the test gateway**

```bash
A="Authorization: Bearer $(cat $V/admin.txt)"; B=http://127.0.0.1:19191
curl -fsS -H "$A" "$B/admin/audit/events?limit=200" > ev.json
python3 - <<'EOF'
import json
ev = json.load(open('ev.json'))['events']
fb = [e for e in ev if e['model_requested'] == 'e2e-fallback']
print('1 sessions', len({e['session_ref'] for e in ev if e.get('session_ref')}), 'classes', sorted({e.get('request_class','') for e in ev}))
print('2 fallback attempts', fb and fb[0]['attempt_count'], fb and fb[0]['fallback'])
print('3 rate limited', [e['deny_code'] for e in ev if e.get('deny_code') == 'key_rate_limited'][:1])
print('7 auto verdicts', sorted({e.get('auto_verdict') for e in ev if e.get('auto_verdict')}))
EOF
grep -c '"session_ref":"s1:' $V/audit.jsonl
for f in $V/audit.jsonl $V/mayu2.log; do grep -Ec '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' $f; done   # raw session IDs are UUIDs
curl -fsS http://127.0.0.1:19191/metrics | grep -c 'session\|key_id'
INFERPLANE_ADMIN_TOKEN=x $V/mayu audit verify --file $V/audit.jsonl > verify.out 2>&1; echo "verify=$?"
```
Expected, per spec success criteria: (1) at least one session and classes including `main`; (2) `attempt_count` 2 and `fallback` true; (3) `key_rate_limited` present; (6) the session-ref count is positive while UUID-shaped strings (raw session IDs) appear 0 times in the audit file and the log, and `/metrics` lines matching `session|key_id` are 0; (5) `verify=0`; (7) verdicts present, or the Denials tab shows "not observed" (record which). Criterion 4 (PII masking) needs a privacy policy; run it with a local policy file only if `examples/policies/` has a masking rule usable as-is, otherwise record it as covered by unit tests and the browser QA, not live.

- [ ] **Step 6: Playwright against the live test console**

Open `http://127.0.0.1:19191/admin/ui/`, enter the test admin token, open Audit, select the fallback trace, and take one screenshot at 1280 px into `$L/audit-live.png` (reuse `audit_qa.py`'s login steps in a short script, run without `-I`). Confirm the waterfall shows two attempt bars and no console errors.

- [ ] **Step 7: Clean up throwaway material**

```bash
pkill -f "$V/mayu serve"; pkill -f 18199; pkill -f "$V/capture.py"
rm -f $V/capture.jsonl $V/real-audit-copy.jsonl $V/key.txt $V/key-rpm.txt $V/admin.txt   # response samples, the audit copy and secrets
ls $V
```
Expected: no `mayu` test processes; only logs, configs and stores remain. Report the results of Steps 1–6 in the ledger and the final message (no commit; nothing in the repo changed).

---

## Self-review notes (plan author)

- Spec coverage: data fields (Tasks 2–7), projection and kinds (8), API (9), console and states (10–11), Mode B (12), docs and the rule exception (13), success criteria and early verification (1, 14). Out-of-scope items are not planned.
- Interfaces: `audit.Finish`/`BeginAttempt`/`NoteAttemptHTTP`/`MarkFirstToken`/`SetAutoMode`, `clientctx.FromRequest`/`Middleware`/`AutoModeWanted`/`AutoModeTap`, `analytics.AuditEventQuerier` and `Run*` keep the same names and signatures in every task that consumes them.
- Known judgment calls an executor may need to make (record as rulings): exact test-helper names in `openaiapi`, `bedrockapi`, `responsesapi`, `internal/server`; `/admin/audit/verify` JSON field names; `NULL` handling of `SUM` in the empty-filter case; `mayu keys create` RPM flag; stub provider config shape.
