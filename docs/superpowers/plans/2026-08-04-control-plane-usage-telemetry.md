# Control-plane usage telemetry — implementation plan

**Spec:** docs/superpowers/specs/2026-08-04-control-plane-usage-telemetry-design.md
**Branch:** feat/control-plane-usage-telemetry
**Revision:** r3 — two P2 consensus gate rounds (codex gpt-5.6-sol, kiro-cli
claude-opus-4.8, agy Gemini 3.1 Pro). Round 1 (unanimous FAIL): eight issue
classes, all confirmed resolved in round 2 by all three reviewers. Round 2
(unanimous FAIL on NEW findings): ack-before-durable data loss, unbounded
memory retention, poison-batch head-of-line blocking, export materialization,
DSN leakage, boot-blocking PG connect — folded into r3 (notably: the CP-side
write queue is DELETED; mayu's FIFO is the single retry store, PG writes are
synchronous-when-healthy). Chair verified code-level claims each round; refuted
with evidence: partial-settle double-count (settle runs once per request).
**Method:** TDD per task, one commit per task (`git commit -s`), all four gates
green each task: build (`CGO_ENABLED=0 go build ./cmd/...`), `go test ./... -race`,
`go vet ./... && gofmt -l .`, `bash tests/run-all.sh`.

Shared invariants every task inherits: money is integer µUSD (never float);
cache 5m/1h tiers stay separate end to end; telemetry must never block or fail
the request path; wire types live in `internal/telemetry` (the ADR-031
consolidation target — both binaries import it); every new inferplaned **data**
endpoint sits behind the existing bearer middleware (static UI assets are
data-free and unauthenticated, the mayu ADR-001/002 posture).

### Task 1: Usage wire types + validation

**Files:**
- Create: `internal/telemetry/usage.go`
- Test: `internal/telemetry/usage_test.go`

`UsageEntry{Team, User, Model string; SpentMicroUSD, InputTokens, OutputTokens,
CacheReadTokens, CacheWrite5mTokens, CacheWrite1hTokens int64}` and
`UsageBatch{Dataplane string; WindowStart, WindowEnd time.Time; Entries
[]UsageEntry}` with JSON tags matching spec D3. `(*UsageBatch).Validate()
error`: non-empty dataplane, WindowStart < WindowEnd, non-negative counts,
team+model required per entry (user may be empty), and **no duplicate
(team, user, model) key within a batch** (a duplicate would violate the
Postgres primary key and turn the batch into a permanently-rejected poison
message). `Model` is the **resolved** model (the name pricing billed), not the
requested alias — pinned by test.

- [ ] Write failing tests: valid batch passes Validate; each violation fails
      with a distinct error message
- [ ] Implement types + Validate; gates green; commit

### Task 2: Aggregator interface + in-memory implementation

**Files:**
- Create: `internal/telemetry/aggregate.go`
- Test: `internal/telemetry/aggregate_test.go`

`Aggregator` interface, three methods:
- `Upsert(ctx, *UsageBatch) error` — **batch-replace** semantics on
  `(dataplane, window_start)` (spec D3's idempotency key): all previously
  stored rows for that key are replaced by the batch's entries atomically, so
  a retried batch — even one with fewer entries — never leaves stale rows and
  never double-counts.
- `Query(ctx, QueryFilter) (QueryResult, error)` — `QueryFilter{Team, User,
  Model string; Since, Until time.Time; GroupBy string}` (`GroupBy` ∈
  team|user|model); `QueryResult{TotalMicroUSD int64; Rows []QueryRow}` with
  `QueryRow{Key string; SpentMicroUSD, InputTokens, OutputTokens,
  CacheReadTokens, CacheWrite5mTokens, CacheWrite1hTokens int64}` — the
  contract Tasks 7, 9, 11 build on.
- `Rows(ctx, since, until time.Time, fn func(StoredRow) error) error` —
  finest-granularity raw rows for Task 9's export, delivered through a
  callback so no implementation ever materializes an unbounded slice
  (`StoredRow` = UsageEntry + dataplane + window bounds; Postgres iterates the
  cursor, memory iterates the map).

`NewMemoryAggregator(retention time.Duration)` — mutex-guarded map keyed by
`(dataplane, window_start)` with **bounded retention**: on every Upsert,
windows older than `retention` (default 24h) are evicted, so a long-running
daemon never grows without bound (full history is Postgres's job — Task 8).
The test suite is written against the interface so Task 8 reruns it verbatim
against Postgres (Postgres runs with retention effectively unbounded).

- [ ] Write failing interface tests: upsert-twice-counts-once; a retried batch
      with FEWER entries leaves no stale rows; group-by sums; time-range filter
      (inclusive start, exclusive end); Rows returns raw granularity; µUSD sums
      exact; windows older than retention are evicted on the next Upsert
- [ ] Implement memory aggregator; gates green; commit

### Task 3: mayu-side window collector

**Files:**
- Create: `internal/telemetry/collector.go`
- Test: `internal/telemetry/collector_test.go`

`NewCollector(dataplane string)` — the constructor takes the dataplane id (the
same value `ControlPlaneConfig.Dataplane` resolves to, hostname-suffix default)
so every drained batch carries it. `Record(team, user, model string,
u pricing.Usage, costMicros int64)` folds into the current window's
`(team, user, model)` bucket; `Drain(now time.Time) *UsageBatch` (nil when
empty) closes the window, stamps `Dataplane`, and opens the next. Record is
called concurrently from every ingress handler — mutex-guarded, with a
concurrent-Record test so `-race` actually exercises it. Imports
`internal/pricing` only for the `Usage` struct (leaf-safe).

- [ ] Write failing tests: two Records with the same key fold; distinct keys
      stay separate; Drain returns correct window bounds + dataplane id and
      empties state; cache 5m/1h counts land in the right fields; parallel
      Records from multiple goroutines fold correctly under -race
- [ ] Implement collector; gates green; commit

### Task 4: Wire Record into the ingress settle paths

**Files:**
- Modify: `internal/server/anthropicapi/messages.go`
- Modify: `internal/server/openaiapi/chat.go`
- Modify: `internal/server/bedrockapi/invoke.go`
- Modify: `internal/server/server.go`
- Modify: `cmd/mayu/gateway.go`
- Test: `internal/server/anthropicapi/messages_test.go`
- Test: `internal/server/openaiapi/chat_test.go`
- Test: `internal/server/bedrockapi/invoke_test.go`

Each ingress handler gets an optional `*telemetry.Collector` (nil = no-op — the
standalone default stays behaviorally identical). Exactly one
`Record(p.Team, p.Owner, model, usage, cost)` call per settle, in the settle
wrapper where Principal + usage + cost are already in scope; the recorded
`model` is the **resolved** model (what pricing billed). Streaming
partial-settle paths record at the same point the partial cost is settled —
settle runs once per request (full or partial), so exactly one entry per
request; pinned by test. Handler construction lives in
`internal/server/server.go` (`DataMux`) — it grows the collector parameter;
`cmd/mayu/gateway.go` passes it (nil when `control_plane` absent).

- [ ] Write failing tests per ingress: a served request lands one collector
      entry carrying the settled cost; a streamed request that dies mid-stream
      lands exactly one entry (partial settle); nil collector serves unchanged
- [ ] ADR-030 regression fixture: an ingress response fixture whose
      `schema.Usage` carries the cache_creation 5m/1h split must produce a
      collector entry with those exact tier counts (covers the historically
      buggy schema.Usage → pricing.Usage handler mapping)
- [ ] Wire through DataMux + gateway.go; gates green; commit

### Task 5: mayu usage pusher (bounded retry buffer)

**Files:**
- Create: `internal/proxy/usagepush.go`
- Test: `internal/proxy/usagepush_test.go`
- Modify: `internal/metrics/metrics.go`
- Modify: `cmd/mayu/gateway.go`
- Test: `internal/metrics/metrics_test.go`

`UsagePusher{URL, Token string; Collector *telemetry.Collector}`: every 60s
Drain → append to a bounded FIFO (cap 60 windows ≈ 1h) → **flush the whole
backlog oldest-first**, stopping at the first **retryable** failure (so the
buffer actually drains after an outage instead of shrinking by at most one per
tick). Failure classification prevents poison-batch head-of-line blocking:
**4xx = permanent** (the batch will never be accepted — drop it and count it),
**5xx / network error = retryable** (keep and retry next tick; this is the
retry leg the control plane relies on — Task 8 returns 503 while Postgres is
down). The HTTP client has an explicit timeout (10s, same posture as the
Syncer) so a hung POST can never stall future drains or shutdown. On overflow
drop the oldest and log + increment a Prometheus counter
(`inferplane_usage_windows_dropped_total`, no team/key labels) — never
silently. The counter is registered in `internal/metrics` like every other
counter. Runs in its own goroutine with the same lifecycle as the Syncer; only
started when `control_plane` is configured.

- [ ] Write failing tests (httptest): success clears the buffer; after N
      retryable-failed ticks, one successful tick drains all N buffered
      windows; a 400/413 response drops that batch (counted) and the NEXT
      batch still flushes; a hung server does not block past the client
      timeout; the 61st failed window drops the oldest and the counter
      increments
- [ ] Implement pusher + metrics counter + gateway wiring; gates green; commit

### Task 6: inferplaned POST /v1alpha1/usage (mounted regardless of --policies)

**Files:**
- Create: `internal/controlplane/usage.go`
- Test: `internal/controlplane/usage_test.go`
- Modify: `internal/controlplane/controlplane.go`
- Modify: `cmd/inferplaned/main.go`

Today `main.go` constructs `controlplane.Server` only when `--policies` is set;
telemetry must not depend on that. Restructure: the usage handler +
aggregator are constructed and mounted **always** (a telemetry-only inferplaned
is valid); the policy `Server` remains conditional. Both share the same bearer
`auth` middleware (extracted so the usage handler uses it whether or not the
policy Server exists — the loopback-only unauthenticated guard already in
main.go applies to both). Decode + `Validate()` (400), `http.MaxBytesReader`
(4 MiB — batches carry per-(team,user,model) rows and can outgrow the 1 MiB
sync bound), distinguishing `*http.MaxBytesError` → 413 from generic decode
errors → 400. `Aggregator.Upsert`; duplicate batch → 200 (idempotent);
**an Upsert error (e.g. Postgres down in Task 8's durable mode) → 503** so
mayu's FIFO keeps the batch and retries — the ack means durable.

- [ ] Write failing tests: valid batch → 200 and queryable; duplicate batch
      doesn't double; bad token → 401; malformed body → 400; oversized → 413;
      failing aggregator → 503; usage endpoints live WITHOUT --policies
- [ ] Implement handler + always-on mounting + main.go restructure; gates
      green; commit

### Task 7: inferplaned GET /v1alpha1/usage + integration round trip

**Files:**
- Modify: `internal/controlplane/usage.go`
- Test: `internal/controlplane/usage_test.go`
- Test: `internal/proxy/usagepush_test.go`

Query params `team`, `user`, `model`, `since`, `until`, `group_by` (default
`team`; invalid value → 400). Response: `QueryResult` (totals + grouped rows),
integer µUSD throughout. Plus the spec's integration test: a real
`UsagePusher` draining a real `Collector` into the real POST handler over
httptest, then `GET /v1alpha1/usage` returns the recorded spend — the full
mayu→inferplaned round trip with no stubs between the seams.

- [ ] Write failing tests: seeded windows → correct sums per group_by; filters
      compose; range bounds inclusive-start/exclusive-end; invalid group_by 400
- [ ] Integration round trip: Collector.Record → pusher tick → POST handler →
      aggregator → GET query returns the exact µUSD and cache tiers recorded
- [ ] Implement query handler; gates green; commit

### Task 8: Opt-in Postgres persistence (durable layer BEHIND the memory aggregator)

**Files:**
- Create: `internal/telemetry/postgres.go`
- Test: `internal/telemetry/postgres_test.go`
- Modify: `cmd/inferplaned/main.go`

Spec failure posture (D2/failure): a PG outage must not take queries down, and
no acknowledged batch may be lost. **The retry store is mayu's FIFO, not a
control-plane-side queue** (round-2 gate: an async CP-side queue acks before
durability and silently loses up to its depth on an ECS task replacement —
the exact deployment reality the spec cites). So:

- `NewPostgresAggregator(dsn)` — pgx (`jackc/pgx/v5`, **already a direct
  dependency** used by `internal/bodystore/postgres.go`; no go.mod change).
  **Lazy connect**: construction never dials, so a PG outage cannot block
  inferplaned boot; the first successful connection runs the `usage_windows`
  DDL (portable types, advisory-lock-serialized CREATE like bodystore) and
  creates a secondary index on `(window_start)` (the PK starts with
  `dataplane`, so time-range dashboard queries need it). `Upsert` = one
  transaction: `DELETE` the `(dataplane, window_start)` key then `INSERT` the
  batch (batch-replace, matching Task 2 semantics). Every returned error is
  **wrapped with the DSN redacted** (pgx connect errors can embed credentials)
  — pinned by a regression test.
- `NewDurableAggregator(mem, pg)` — `Upsert` is **synchronous write-through
  when PG is configured: PG commit first, then memory, then ack**. If the PG
  write fails, Upsert returns the error and the POST handler (Task 6) returns
  **503** — mayu keeps the batch in its FIFO and retries (Task 5 treats 5xx as
  retryable), so durability is acknowledged-or-retried, never assumed. There
  is NO CP-side write queue. `Query`/`Rows` serve from PG when healthy (full
  history; the synchronous write also closes the read-after-write gap), fall
  back to memory (retention window) on PG error — queries keep serving through
  an outage.
- Config: fixed env var `INFERPLANED_USAGE_DSN` (the `INFERPLANED_TOKEN`
  precedent — inferplaned has no config file; the spec's `usage_store` block
  shape is amended accordingly, recorded in ADR-036). Set → durable
  aggregator; absent → memory only (default unchanged).

- [ ] Rerun the Task-2 interface suite against Postgres, skipping unless
      `INFERPLANE_TEST_PG_DSN` is set (CI stays memory-only)
- [ ] Durable-aggregator tests (memory + a failing fake PG): a failed PG write
      surfaces the error (no silent memory-only ack); queries fall back to
      memory during the outage and return to PG after recovery; construction
      with an unreachable DSN does not block; DSN never appears in any error
      string
- [ ] Implement; verify `CGO_ENABLED=0 go build ./cmd/...` stays green; commit

### Task 9: Usage export (CSV/JSON)

**Files:**
- Modify: `internal/controlplane/usage.go`
- Test: `internal/controlplane/usage_test.go`

`GET /v1alpha1/usage/export?since=&until=&format=csv|json` — finest-granularity
rows via the `Aggregator.Rows` callback (no grouping), behind the bearer
middleware, written row-by-row to the response as the callback fires (真
streaming — nothing materializes server-side). `since`/`until` are
**required** (400 when absent) so an export is always an explicitly bounded
range. Analysis/migration convenience, not DR.

- [ ] Write failing tests: CSV header + row count; JSON round-trips; default
      format json; missing since/until → 400; 401 without token
- [ ] Implement export; gates green; commit

### Task 10: Config export (GovernancePolicy YAML)

**Files:**
- Create: `internal/controlplane/export.go`
- Test: `internal/controlplane/export_test.go`
- Modify: `internal/controlplane/controlplane.go`

`GET /v1alpha1/config/export` — **behind the bearer middleware** — renders the
currently-distributed policy set back to multi-document
`apiVersion: inferplane.dev/v1alpha1` YAML via `sigs.k8s.io/yaml` (existing
dependency). Secret-free by construction (policies carry refs only). The
import endpoint is deliberately NOT in this plan (reserved — spec D5; import
today = place files in `--policies`).

- [ ] Write failing tests: load the examples policies → export → parse the
      output back through the policy loader → semantically equal round trip;
      401 without token
- [ ] Implement export + Mount; gates green; commit

### Task 11: inferplaned minimal web console (read-only)

**Files:**
- Create: `internal/controlplane/ui/ui.go`
- Create: `internal/controlplane/ui/static/index.html`
- Create: `internal/controlplane/ui/static/app.js`
- Create: `internal/controlplane/ui/static/style.css`
- Test: `internal/controlplane/ui/ui_test.go`
- Modify: `internal/controlplane/controlplane.go`

mayu-console rules verbatim (ADR-001/002): static assets are **data-free and
served unauthenticated** at `/ui/` — this is the documented, deliberate posture
(AGENTS.md known-false-positive list), resolving the "UI behind bearer"
paradox: the browser loads the shell, the operator enters the bearer token
once, JS holds it in memory only (never localStorage/sessionStorage/cookies)
and calls the **authed** `GET /v1alpha1/usage`. Vanilla JS, `go:embed`, CSP
`default-src 'self'`, no inline style/handlers. Team table → period spend →
model breakdown. No charts (YAGNI).

- [ ] Write failing tests: CSP header on every UI response; embedded assets
      served at /ui/; served bytes contain no secret AND no
      localStorage/sessionStorage/document.cookie usage in app.js (the
      token-storage discipline, mechanically checked)
- [ ] Implement console; `bash tests/run-all.sh` structure checks green; commit

### Task 12: Docs sync + ADR

**Files:**
- Create: `docs/decisions/ADR-036-control-plane-usage-telemetry.md`
- Modify: `docs/reference/api.md`
- Modify: `docs/reference/data.md`
- Modify: `internal/CLAUDE.md`
- Modify: `CLAUDE.md`
- Modify: `docs/architecture.md`
- Modify: `docs/superpowers/specs/2026-08-04-control-plane-usage-telemetry-design.md`

ADR-036 condenses the spec: decision + rejected alternatives (DuckDB/cgo,
DynamoDB/CNCF, local-SQLite/stateless, sync-channel-extension) **and records
the two P2-gate amendments**: `INFERPLANED_USAGE_DSN` env var replaces the
spec's `usage_store` config block (inferplaned has no config file), and
Postgres composes as a durable layer behind memory (never either/or). The spec
file gets the matching D2 amendment note. Reference docs gain the new
endpoints and the usage_windows schema; CLAUDE.md's structure section notes
internal/telemetry is now live.

- [ ] Write ADR-036 + amend spec D2 + sync the five reference/context docs;
      commit

## Out of scope (enforced by scope guard)

Anything not listed above — in particular: no `providers/` changes, no
sync-heartbeat protocol changes, no HA work, no policy-editing UI, no mayu CLI
`status` command, no audit-record or hash-chain changes.
