# Roadmap: closing the five operational gaps vs central-proxy gateways

Status: proposed (2026-07-31), still fully open as of 2026-08-14 — none of
the five items below have shipped. Source: critical comparison against LiteLLM —
the split architecture's costs are already being paid (fleet of data planes,
version skew management, distributed accounting); these five items are where
the benefits are still only partially collected.

[Enterprise product strategy](enterprise-strategy.md) is the canonical source for
target market, product contracts, priorities, and production-release gates. This
roadmap tracks execution status and retains the original five-gap work breakdown.

## Purpose alignment (2026-08-14)

`CLAUDE.md` → Core Purpose lists five goals. This table is the internal
priority lens the LiteLLM-gap framing above doesn't give you — it's ordered
by which goal each gap blocks, not by feature parity with a competitor. Its
scope is broader than the five sprint items below: a row can be ✅ even
though none of sprints S1-S3 have shipped, because some goals (e.g. #5) were
already met by earlier work (ADR-031) outside this roadmap.

| Purpose | Status | Evidence |
|---|---|---|
| #1 A single entry point for Claude Code/OpenCode/Codex | ✅ done, with caveats | REAL-client verification (2026-09-02, [docs/verification/coding-agents.md](verification/coding-agents.md)): Claude Code 2.1.258, OpenCode 1.18.26, AND Codex 0.152.1 each completed a real turn through a running mayu — served, settled, hash-chain-audited, zero client patches. Codex initially exposed that current Codex removed `wire_api: "chat"` and requires the OpenAI Responses API; closed the same day by `internal/server/responsesapi` (`POST /v1/responses`, an adapter over the chat pipeline, built against Codex's own captured request — that capture is now the package's test fixture). Caveats: verified against a mock upstream (a real-provider session is the remaining step), and the adapter drops Responses-only params with disclosure (reasoning replay, non-function tools) |
| #2 Per-user model choice | ✅ done | User-subject `modelAccess` rules are enforced: `Store.ModelAllowed` (`internal/policy/store.go`), wired into the router via `SetPolicyGate` in `cmd/mayu/gateway.go`. (Per-user *rate* is a separate, still-blocked item — see #4b; per-user *budget* is enforced as of ADR-042 Phase 3.) |
| #3 Cost-driven model substitution via policy (routing) | ✅ done, with caveats (ADR-041) | `routing.budgetTiers` is enforceable: `internal/policy/store.go` `checkEnforceable` now rejects only the cache-affinity half of `routing`; the control plane judges utilization globally from the ADR-034 ledger (`internal/controlplane/controlplane.go` `handleSync`) and latches the active tier per budget window (`internal/tier.Latch`); mayu applies it at ingress via `router.SubstituteTier`, never widening access or turning into a denial. Config-level `model_fallbacks` (`internal/router/router.go` `ResolveModel`) remains the separate availability-triggered substitution. Caveats: the control-plane tier latch now keys on the referenced budget rule's real `windowID` (item ② shipped); the interim calendar-month-UTC derivation remains only for standalone mode's latch; providerstore/UI pricing fields for `openai_compatible` GPU targets (ADR-041 item 6) and the full two-plane e2e (item 7) are follow-ups. |
| #4a Team budget + block | ✅ done, with caveats | ADR-034 lease pattern bounds team-level overspend across data planes when a control plane is attached (worst case = Σ outstanding grants, not exact; window edges are approximate — ADR-034 §Known limits). Per-key budgets are not lease-managed. Standalone `mayu` (no control plane) gets no lease at all — budget is plain in-memory there, like rate. |
| #4b Per-user budget/rate | 🔶 partial | *Budget* is unblocked (ADR-042 Phase 3): `checkEnforceable` (`internal/policy/store.go`) now rejects only user-subject *rate*; user-subject budget rules are merged by `mergeUserLimits`/`Store.UserLimits` and enforced by the Governor via `governance.SetUserLookup`/`UserPolicy`. *Rate* stays blocked — a per-user rate limit needs the rate-share model (item ① below). And per-user budget has no lease: a user-subject rule is excluded from the control-plane ledger and the consumption report, so with N data planes a user's effective cap is up to N× the configured value (ADR-042 §Accepted limitation, Phase 3) |
| #4c Rate/quota global accuracy under horizontal scale | 🔶 partial | Team policy RATE rules are globally bounded when a control plane is attached (ADR-043 rate shares, equal-split v1, shipped 2026-09-01): the fleet aggregate stays ≤ the configured rpm/tpm (two-gateway e2e `cmd/mayu/rateshare_e2e_test.go`). Still per-replica: token quotas (tokens/day), per-key rate limits, config/keystore team rate in standalone mode, and the proportional-to-EWMA split (item ① below's full shape) |
| #4d Spend visibility | ✅ done | `internal/analytics` + console + `GET /admin/logs` (`analyticsapi.LogsHandler`, backed by the same analytics index — its `events` rows carry `cost_micros` per request, `internal/analytics/index.go:40`) |
| #5 No SPOF (control/data plane split) | ✅ done | ADR-031 — scoped to the control plane not gating the inference path; it does not mean any one `mayu` instance is itself highly available (see #4c and "Current limits") |

**The known tension:** #4c and #5 pull against each other — see `CLAUDE.md` →
Core Purpose. HA work here means closing that gap, not merely adding
replicas; a naive multi-replica deployment currently breaks #4c further
without an accurate shared rate/quota store.

Sprint plan (each phase = separate PR(s), reviewed before the next):

| Sprint | Items | Why together |
|---|---|---|
| S1 (~1 wk) | ① global rate limits + ② durable ledger & window epochs | Both change the sync protocol — one protocol revision, not two |
| S2 (~1 wk) | ④ `mayu doctor` + ③ phase 1 (version visibility) | Pure observability, no protocol risk, unblocks real-world debugging |
| S3 (1–2 wk) | ③ phase 2 (signed self-update) + ⑤ embeddings lane | Release pipeline work + first non-chat modality |

---

## ① Global rate limits via rate shares — ✅ both halves shipped (equal split 2026-09-01, EWMA split 2026-09-02) as ADR-043

**Shipped** ([ADR-043](decisions/ADR-043-global-rate-shares.md)): the control
plane divides each team rate rule's rpm/tpm equally among live data planes
(liveness = 3× heartbeat, the lease horizon; min-1 floor so no plane
starves) and hands each its share in the heartbeat
(`SyncResponse.rateShares`, additive). mayu clamps the governor's team
rpm/tpm to min(policy limit, share) in the same team-lookup closure as the
budget allowance clamp — narrows-only, so a compromised control plane can
only reduce throughput. Failure semantics: FailOpen keep-last. Two-gateway
e2e (`cmd/mayu/rateshare_e2e_test.go`): the 429 appears at the global limit,
not N× it.

**Shipped (EWMA half, 2026-09-02):** the split now follows demand. mayu
counts settled traffic per team (`governance.Governor.FlowTotals` —
requests + total tokens, cache tiers included; denied requests earn no
share), the syncer differentiates it per heartbeat and EWMA-smooths
(α=0.5) into `SyncRequest.flows` (additive; a deliberate deviation from
the sketch's `recentRPM`/`recentTPM` inside `ConsumptionReport`, since
reports exist per BUDGET rule and a rate-only team would have had nowhere
to report). The control plane reserves HALF each rule's limit as the
equal floor — an idle plane can always start working without waiting a
rebalance — and divides the other half proportionally to reported flow:
Σ ≤ limit by construction, min-1 floor kept, and a flow-less fleet
degrades to exactly the v1 equal split. **Still open:** per-user rate
rules (need user-keyed shares — a cardinality design of its own), token
quotas, per-key rates, standalone mode.

### Original design (both halves now implemented — kept for rationale)

**Gap.** `rpm`/`tpm` enforce against per-proxy in-memory buckets
(`limiter.NewMemory`): a team capped at 300 rpm with 20 connected data planes
can actually reach ~6,000 rpm. Budgets were globalized by leases (ADR-034);
rate was not. LiteLLM gets this "free" via Redis.

**Why budgets' lease design doesn't transfer as-is.** A budget is a stock
(cumulative, settles later); rate is a flow (per-minute, must be right *now*).
Cumulative allowances don't mean anything for a flow — what can be divided is
the *rate itself*.

**Design — rate shares.** The control plane divides each rate rule's global
rpm/tpm among currently-active data planes and hands each a share in the
existing heartbeat:

- `SyncResponse` gains `rateShares: [{policy, rule, team, rpm, tpm, expiresAt}]`.
- Split policy: proportional to each plane's reported recent consumption
  (EWMA over the last few heartbeats, reported in `ConsumptionReport` as
  `recentRPM`/`recentTPM`), with an equal-split floor so an idle plane can
  always start working without waiting a rebalance. Σ shares ≤ global limit,
  always.
- mayu clamps the governor's team `RatePerMin`/`TokensPerMinute` to its share
  (same seam as the budget allowance clamp — the team-lookup closure).
- Failure semantics: rate rules are FailOpen in practice — on lease expiry
  keep the last share (never widen to the global limit, never zero). No
  hard-cap analogue: a rate limit protects throughput, not money.
- Rebalance cadence = the heartbeat (10s default): a plane going quiet
  releases its share within one lease horizon (3× renew), same mechanism as
  budget grant release.

**Work items.**
1. Protocol: `RateShare` grant type + EWMA fields in reports (additive,
   `omitempty` — old planes simply don't receive/report them).
2. Control plane: per-rule share ledger keyed on the active-dataplane set
   (reuses `dpInfo.LastSeen` liveness from ADR-034 review fixes).
3. mayu: share table next to `LeaseTable`; clamp wiring; tests: 2-plane
   proportional split, idle floor, dead-plane share release, Σ ≤ limit
   invariant under churn.
4. e2e: two gateways against one control plane, 429 appears at the *global*
   limit, not N× it.

**Risks.** Share rebalancing lag (≤1 heartbeat) lets a suddenly-hot plane 429
briefly while holding a small share — acceptable; document. Bursty split
(burst = share) mirrors existing team-bucket burst semantics.

---

## ② Durable ledger + control-plane-owned budget windows — ✅ both halves shipped (durability 2026-09-01, window epochs 2026-09-02)

**Shipped (window epochs):** the control plane owns the budget window.
`windowIDFor(period, now)` computes a UTC calendar epoch id (`"2026-09"` /
`"2026-09-02"`); every heartbeat rolls each ledger to the current epoch
(`ruleLedger.roll` — spend and allowance drop WHOLESALE when the id changes,
because the id changed, not because a heuristic guessed), every
`LeaseGrant` is stamped `windowID`, and a `ConsumptionReport` stamped with
a previous epoch is refused. mayu bridges the UTC-vs-`budget_timezone`
phase difference by baselining its local counter at each OBSERVED epoch
change: reports send (counter − baseline) and the lease clamp allows
(allowance + baseline); a counter falling below its baseline (the local
boundary passed) resets it. The tier latch now keys on the referenced
budget rule's real epoch (the interim `tier.WindowKey` derivation remains
only in standalone mode, which has no control plane to own an epoch), and
ledger-store rows persist the epoch so a restart never resurrects a
rolled-over window's spend. Decrease-detection remains ONLY as the
fallback for epoch-less reports from pre-epoch builds — both wire fields
are additive/omitempty, so mixed fleets degrade to exactly the old
behavior. Still open (accepted): standalone mode keeps rolling local
calendar windows (documented behavioral difference), and per-RULE spend
reporting (several rules in one window share a team counter, conservative)
stays as noted in `internal/proxy/sync.go`.

**Shipped (durability):** `INFERPLANED_LEDGER_PATH` (env-only, opt-in;
unset = in-memory, byte-identical) attaches a SQLite `LedgerStore`
(`internal/controlplane/ledgersqlite.go` — modernc, CGO stays off, WAL +
busy_timeout per the keystore convention; interface mirrors `bodystore`'s
sqlite/postgres split so Postgres can land later). Per-(rule, dataplane)
spent/allowance and data-plane last-seen persist write-behind on each
heartbeat (one transaction; a save failure logs and never fails the
heartbeat); boot load restores rows into rules that still exist with an
unchanged `period` and restores liveness so outstanding grants keep
counting; the 24h prune deletes a dead plane's rows. Restart resumes
grants exactly — `TestLedgerStoreRestartPreservesSpendAndOutstanding`
pins that a fully-committed budget stays committed across a restart
instead of re-granting spent money. The cumulative-report self-healing
stays as the fallback.

### Original design (both halves now implemented — kept for rationale)

**Gap.** The lease ledger is in-memory: restart re-learns from cumulative
reports, grants issued moments before a crash are re-derived, and — the real
correctness hole — budget windows are per-data-plane tumbling windows, so
"the monthly team budget" is only approximately global. Rollover is detected
heuristically (a cumulative report DECREASING), and pruned dead planes drop
their window spend entirely. ADR-041's budget-tier latch
(`internal/tier.Latch`) currently derives its own interim window key
(calendar-month UTC) rather than waiting on this item — once a real
`windowID` exists it should replace that derivation.

**Design — the control plane owns the window.**
- Each budget rule gets a control-plane-computed `windowID` (calendar month
  UTC, `"2026-08"` — operator-legible beats rolling-30d) carried in every
  grant and echoed in every report.
- mayu keys its cumulative counter by `(rule, windowID)`; when the grant's
  windowID changes, it starts a fresh counter — no more decrease-detection
  heuristics, no more per-plane phase drift. (The local `budget.BudgetStore`
  gains window-id-keyed entries; the 30-day-duration window stays for
  standalone mode.)
- Ledger rows become `(policy, rule, windowID, dataplane) → spent, allowance`.
  Old windows are dropped wholesale at rollover — cleanly, because the ID
  changed, not because a heuristic guessed.

**Design — durability.**
- SQLite file for inferplaned (modernc driver — already a dependency, CGO
  stays off), single-writer, write-behind on each heartbeat (QPS is
  heartbeat-rate × planes; trivial). Tables: `lease_ledger`, `dataplanes`.
- Restart: load ledger → grants resume exactly; the cumulative-report
  self-healing stays as a fallback, stops being the *only* mechanism.
- HA (multiple control-plane replicas) is explicitly out of scope here; the
  interface mirrors `bodystore`'s sqlite/postgres split so a Postgres backend
  can land later without protocol changes.

**Work items.** Store interface + SQLite impl; windowID through
`LeaseGrant`/`ConsumptionReport`; mayu window-keyed counters; rollover
integration test (clock-injected); restart-preserves-ledger test; remove the
decrease-detection path (superseded).

**Risks.** Local budget store schema touch is the riskiest edit (shared with
standalone mode) — gate with the full e2e suite. Calendar-month vs the
existing rolling-30d standalone semantics must be documented as a behavioral
difference between modes until standalone also adopts windowIDs.

---

## ③ mayu version channel + signed self-update (ADR candidate — unassigned; ADR-038 has since shipped as the control-plane policy store)

**Gap.** mayu on developer laptops is an endpoint-agent fleet with no update
mechanism. Version skew is *detected* (heartbeat + `/v1alpha1/dataplanes`)
but the tail of stale planes can only shrink by hand today.

**Phase 1 — visibility & advice — ✅ shipped 2026-09-01.**
- Version embedded via `-ldflags -X main.version` (Dockerfile `ARG VERSION`,
  un-stamped builds report `dev`); `SyncRequest.version` (additive,
  omitempty); `/v1alpha1/dataplanes` shows the per-plane version next to
  apiVersions (`internal/controlplane` `dpInfo.Version`) — the operator's
  "can I ship this rule yet" check has its second axis.
- `INFERPLANED_MINIMUM_VERSION` (+ optional `INFERPLANED_UPDATE_URL`, env-only
  per the `INFERPLANED_TOKEN` precedent): a heartbeat from an older — or
  unparseable, e.g. `dev` — build gets `updateAdvice {minVersion, url}` in
  its SyncResponse (`versionBelow`, lenient numeric compare); mayu logs a
  loud warning once per distinct advice (`Syncer.OnUpdateAdvice`, deduped
  across heartbeats) and `mayu version` prints the build version +
  supported apiVersions. Advice only — nothing auto-applies. (The original
  sketch's `mayu version --check` remote query is folded into Phase 2's CLI
  work; the serve-path warning covers the fleet case.)

**Phase 2 — signed manual update — ✅ binary half shipped 2026-09-02.**
- `mayu update --url <base> [--yes]` (`cmd/mayu/update.go`): fetches
  `manifest.json` + `manifest.json.sig`, verifies the ed25519 signature
  against the public key stamped at build (`-ldflags -X
  main.updatePubKeyHex=…`; an UN-stamped build refuses — fail closed),
  checks the artifact's manifest-pinned sha256, then swaps atomically
  (sibling write → rename, previous kept as `.old`, rollback on a failed
  rename) — nothing restarts automatically. One signature covers the
  manifest; artifacts are hash-pinned inside it, so a mirror cannot swap
  one platform's binary without breaking the chain. URL rule:
  https-or-loopback (the ADR-040 transport precedent). Without `--yes` it
  verifies and reports only. Deviation from the sketch, recorded:
  stdlib `crypto/ed25519` detached signatures instead of minisign/cosign —
  the same primitive, zero new dependencies; `scripts/signrelease`
  (genkey/sign) is the pipeline-side tool and the round trip is verified
  end to end (signrelease-signed dir consumed by a stamped mayu build).
  **Still open:** wiring signrelease into the actual release CI
  (goreleaser job + key custody), and `--channel` (one manifest URL per
  channel serves the need for now). K8s stays excluded — the image
  pipeline owns node upgrades there.

**Phase 3 — auto-update channel (later).** Idle-window self-update with
health self-check + rollback to `.old` on boot failure.

**The security constraint that shapes all of it.** The control plane must
never be able to push executable content — only a *version pin*, which the
data plane independently verifies against the embedded release public key.
Otherwise a control-plane compromise is RCE on every laptop. This is
non-negotiable and goes in the ADR's security section.

---

## ④ `mayu doctor` (S2, ~1–2 days) — ✅ shipped 2026-09-01 (CLI + remote snapshot)

**Gap.** Distributed debugging: "it fails only on my machine" requires
inspecting that node's state, and today that means grepping logs.

**Shipped** (`cmd/mayu/doctor.go`): `mayu doctor --config <path> [--json]
[--no-probe]` — config parse vs secret-ref resolution diagnosed separately,
policy source (files parsed+counted / control plane / none), listen-port
status, pricing coverage (`live.UnpricedTargets`, the ADR-030 guard),
provider construction + ADR-014 `HealthChecker` probes (`--no-probe` skips
the real upstream calls; a broker-mode construction failure is a warn with
context, since it needs the serve-time fetcher), and control plane
reachability/latency via `/readyz`, apiVersion overlap, and a token check
against `/v1alpha1/dataplanes` — deliberately never a sync POST, which
would register the doctor run as a data plane in the lease ledger. Every
check reuses the exact function the gateway runs at boot, so doctor can
never disagree with `serve`. **Also shipped:** `GET /admin/debug/governance`
(`internal/server/debugapi`, same AdminAuth + secret-free discipline as
`/admin/config`) — the running gateway's governance snapshot: policy
source, per-team usage (TEAM subject only, key- and user-free by
construction), ADR-034 lease windows, and ADR-043 rate shares; asserted
live in the two-gateway rate-share e2e. **Still open:** clock-skew vs
control plane (needs a server timestamp in the heartbeat).

**Original design** — one command, human output + `--json` for support tickets:

- config: parse/validation result, which policy source (files vs control
  plane), secret refs resolvable (never the values);
- control plane: reachability, auth OK, latency, applied generation vs
  server generation, pending rejections;
- governance: applied policies per team, lease table (allowance/spent/expiry,
  from `Governor.UsageOf` + `LeaseTable`), rate shares once ① lands;
- providers: connection probes (reuse `configapi/probe.go`'s SSRF-guarded
  prober), pricing coverage (reuse `live.UnpricedTargets`);
- environment: version, supported apiVersions, clock skew vs control plane
  (lease expiry math depends on it), listen-port conflicts.

Also `GET /admin/debug/governance` (admin-auth, secret-free DTO — same
redaction posture as `/admin/config`) so an operator can pull the same
snapshot remotely from a machine they can't shell into.

**Risk.** Leakage — every field goes through the existing secret-free view
discipline; `key_id`/owner stay out of the JSON by default.

---

## ⑤ Provider coverage: embeddings first — ✅ v1 shipped 2026-09-02 (lane pattern + openai_compatible)

**Shipped:** `POST /v1/embeddings` (`internal/server/embeddingsapi`) as the
designed governed passthrough: KeyAuth → model RBAC (with the post-routing
re-check) → region lock → PII egress ceiling (blocked/internal-only
enforced; external-masked/unmodified REFUSE — this lane has no
masker/detector) → PreCheck with cost reservation → priority fallback over
`providers.Embedder` targets only → Settle on `usage.prompt_tokens` at the
(provider, upstream) input rate → hash-chained audit (ingress
"embeddings"). The optional-interface pattern held: `openai_compatible`
opts in with ~50 lines (`embed.go`, verbatim body + top-level model
rewrite), anthropic/bedrock don't implement it and 404 cleanly, zero core
diff to any provider package. The Phase 0a zero-bill fence applies: a 2xx
without usage is refused, never served free (e2e-pinned,
`cmd/mayu/embeddings_e2e_test.go`). **Still open:** the bedrock
Titan/Cohere embed mapping (swappable detail per the design); images/
audio/rerank stay explicit non-goals until the lane pattern earns them.

### Original design (implemented — kept for rationale)

**Gap.** Three provider types, chat-only. The canonical schema is a
Messages-superset — embeddings structurally don't fit it, and forcing them
through it would violate the lossless-round-trip invariant.

**Design — a governed passthrough lane, not a canonical one.**
- New ingress `POST /v1/embeddings` (OpenAI wire shape — the de-facto
  standard clients speak).
- Providers opt in via an *optional* interface (`providers.Embedder`,
  discovered by type assertion) so existing provider packages and the §8
  zero-core-diff rule survive: `openai_compatible` forwards verbatim
  (§4.4 applies trivially), `bedrock` adds Titan/Cohere embed model mapping;
  `anthropic` simply doesn't implement it → clean 404 per model.
- Governance is identical: KeyAuth → canonicalize → modelAccess gate →
  PreCheck → forward → Settle with usage tokens × per-mtok input rate
  (embeddings have no output tokens; pricing table already keys on
  (provider, upstream)).
- Explicitly NOT in this phase: images, audio, rerank — each gets its own
  lane decision later; and no new chat providers until the lane pattern is
  proven (Gemini/Vertex next, via their OpenAI-compat endpoints first).

**Risk.** Scope creep is the failure mode — the lane pattern (optional
interface + governed passthrough) is the deliverable; Titan/Cohere mapping
details are swappable.

---

## Explicitly deferred (so the list stays five)

- **Credential brokering (ADR-040, Accepted — design gate passed)** — inferplaned vends
  short-lived STS Bedrock credentials so `bedrock:Invoke*` leaves
  developer/node IAM entirely (bypassing mayu then yields no credentials).
  Accepted 2026-08-18 after a 3-round 3-AI design gate (10 findings fixed).
  Requires a dedicated `INFERPLANED_BROKER_TOKEN` (never the heartbeat
  token) and auth-mode validation in mayu's config loader.
- Control-plane HA / Postgres ledger backend (interface prepared in ②) — see
  §"HA (multiple control-plane replicas) is explicitly out of scope here"
  above.
- **Data-plane (mayu) shared-state HA** — `keystore`/`limiter`/`budget`
  moving off SQLite/in-memory onto a shared backend so #4c above stops being
  blocked. Maintainer direction as of 2026-08-14 is **Postgres-only**, recorded
  here pending a real ADR (narrower than
  ADR-013's original Postgres+Redis/Valkey design — no Redis dependency is
  planned). Still deferred: implementation has not started, and ADR-013
  itself carries no superseded-by marker yet — a new ADR is needed before
  work begins, and it must resolve what ADR-013 left open (fail-open vs.
  fail-closed on a Postgres outage). See "Purpose alignment" above.
- SSE push stream for policy distribution (poll-at-lease-cadence already
  beats the 60s/15s requirement).
- CRD-watch controller in inferplaned (ADR-035 follow-up).
- User-subject *rate* rules (need a rate-share model — item ① — before the
  ADR-033 gate can accept them; user-subject *budget* shipped in ADR-042
  Phase 3, which narrowed that gate to rate-only).
- Cache-affinity routing engine (the `routing` rule's *affinity* half stays
  rejected until then; the *budgetTiers* half shipped as ADR-041 — it never
  depended on the affinity engine).
