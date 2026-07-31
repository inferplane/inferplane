# Roadmap: closing the five operational gaps vs central-proxy gateways

Status: proposed (2026-07-31). Source: critical comparison against LiteLLM —
the split architecture's costs are already being paid (fleet of data planes,
version skew management, distributed accounting); these five items are where
the benefits are still only partially collected.

Sprint plan (each phase = separate PR(s), reviewed before the next):

| Sprint | Items | Why together |
|---|---|---|
| S1 (~1 wk) | ① global rate limits + ② durable ledger & window epochs | Both change the sync protocol — one protocol revision, not two |
| S2 (~1 wk) | ④ `mayu doctor` + ③ phase 1 (version visibility) | Pure observability, no protocol risk, unblocks real-world debugging |
| S3 (1–2 wk) | ③ phase 2 (signed self-update) + ⑤ embeddings lane | Release pipeline work + first non-chat modality |

---

## ① Global rate limits via rate shares (ADR-036 candidate)

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

## ② Durable ledger + control-plane-owned budget windows (ADR-037 candidate)

**Gap.** The lease ledger is in-memory: restart re-learns from cumulative
reports, grants issued moments before a crash are re-derived, and — the real
correctness hole — budget windows are per-data-plane tumbling windows, so
"the monthly team budget" is only approximately global. Rollover is detected
heuristically (a cumulative report DECREASING), and pruned dead planes drop
their window spend entirely.

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

## ③ mayu version channel + signed self-update (ADR-038 candidate)

**Gap.** mayu on developer laptops is an endpoint-agent fleet with no update
mechanism. Version skew is *detected* (heartbeat + `/v1alpha1/dataplanes`)
but the tail of stale planes can only shrink by hand today.

**Phase 1 — visibility & advice (S2, ~1 day).**
- Embed version via `-ldflags -X` at build; add `version` to `SyncRequest`;
  dataplane view shows the version distribution (the operator's "can I ship
  this rule yet" check gets a second axis besides apiVersions).
- Control plane config `minimumVersion`: sync responses include
  `updateAdvice {minVersion, url}`; mayu logs a loud warning and exposes it
  on `mayu version --check`. Advice only — nothing auto-applies.

**Phase 2 — signed manual update (S3).**
- Release pipeline: goreleaser + minisign/cosign signatures on artifacts;
  public key embedded in the binary at build.
- `mayu update [--channel stable]`: fetch → verify signature → atomic swap
  (write sibling, rename, keep previous as `.old`) → user restarts. No
  root: installs to the user-writable location it runs from. K8s is
  excluded — the image pipeline owns node upgrades there.

**Phase 3 — auto-update channel (later).** Idle-window self-update with
health self-check + rollback to `.old` on boot failure.

**The security constraint that shapes all of it.** The control plane must
never be able to push executable content — only a *version pin*, which the
data plane independently verifies against the embedded release public key.
Otherwise a control-plane compromise is RCE on every laptop. This is
non-negotiable and goes in the ADR's security section.

---

## ④ `mayu doctor` (S2, ~1–2 days)

**Gap.** Distributed debugging: "it fails only on my machine" requires
inspecting that node's state, and today that means grepping logs.

**Design.** One command, human output + `--json` for support tickets:

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

## ⑤ Provider coverage: embeddings first (ADR-040 candidate, S3)

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

- Control-plane HA / Postgres ledger backend (interface prepared in ②).
- SSE push stream for policy distribution (poll-at-lease-cadence already
  beats the 60s/15s requirement).
- CRD-watch controller in inferplaned (ADR-035 follow-up).
- User-subject budget/rate windows (needs per-user governance keys end to
  end; unblocks the ADR-033 gate).
- Cache-affinity routing engine (`routing` rules stay rejected until then).
