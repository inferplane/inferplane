# Review remediation — 2026-09-21

Baseline: `6e3c4dc` (main). Sources: the operator-local `review/kimi-k3`
review of the divergent `401dd20` worktree and `review/fable-51` review of
`5fb3f94`. Those ignored review directories are not prerequisites for this
tracked disposition. Finding IDs below identify the original reports, not
newly verified severity ratings.

## This patch: provider accounting

- **Fable B1:** Anthropic non-streaming Complete and Bedrock InvokeModel now
  refuse malformed successful response JSON or a missing/null usage object
  with a fixed, Anthropic-shaped 502 upstream error. They return no successful
  proxy response and do not echo upstream content in the synthetic error.
  Explicit zero usage remains valid; valid response bytes are unchanged.
  Anthropic non-2xx passthrough and token-count behavior are unchanged.
- **Fable B3:** Bedrock Converse SDK usage with a nonzero flat cache-write
  total and no TTL breakdown now carries `AccountingUncertain` through the
  existing complete/stream adapters. The cheaper-tier pricing estimate is
  unchanged; an explicit zero total needs no TTL split. Negative totals remain
  uncertain, not evidence of an exact settlement.

These changes do not fabricate actual cost for failed calls. A refused upstream
response may still have incurred provider charges, and normal routing may retry
its 502. Local accounting cannot recover omitted provider usage. ADR-045/046
retain uncertain reservations through their existing attempt-finalization path.
A non-nil but incomplete usage object, interruption before any usage frame,
and OpenAI-wire cache-write extensions require separate treatment; this patch
is not a claim of complete financial reconciliation.

The report's assertion that ADR-045 accepts a positive flat cache total as exact
is not true at this baseline: `requestpolicy.authorityUsage` already rejects
positive flat totals without a TTL split. The B3 patch preserves uncertainty
at the provider observation boundary rather than repairing that authority gate.

## Already addressed or superseded on main

| Source | Disposition and evidence |
| --- | --- |
| Kimi F1 | The reviewed competing stateless adapter is not main's architecture. ADR-043 is policy-aware routing; ADR-047 and `internal/responses` are the maintained Responses conversion path. Do not port the abandoned adapter wholesale. |
| Kimi F2 | PR #96 (`a4eec84`) committed foreign-thinking Responses handling and regression tests, plus priced model examples. This establishes a reproducible source fix, not a fresh production acceptance test. |
| Kimi F3–F6, F8 | Findings refer to the old adapter/conversion implementation. Their exact anchors cannot be treated as current main defects. Current role ordering, custom tools, buffering and service-tier contracts still need their own targeted assessment. |
| Kimi harness-CI recommendation | `.github/workflows/ci.yml` already runs `bash tests/run-all.sh`. |
| Fable B5 | PR #97 (`6e3c4dc`) preserves stricter base RPM/TPM under policy overlays. |
| Fable S2, D2 | PR #97 qualifies audit guarantees and tracks the control-plane bypass threat-model documentation. This does not implement required-sink durability. |

## Remaining priorities — not closed by this patch

1. **Fable A1 / A4 (P0):** enforce guardrail compatibility across every provider
   attempt/fallback, and provide an explicit default Helm audit sink with a
   deliberate zero-sink configuration contract. Guardrail configuration alone
   must not be presented as proof of application. Audit sink validation requires
   migration/fixture coverage; merely adding a startup error is not sufficient.
2. **Fable B2 / R4, A2:** test interruption before usage without invented charges;
   define audit sink failure, WAL recovery and readiness behavior together.
3. **Fable B4 / B6:** establish supported OpenAI cache-write wire fields with
   fixtures and cover pricing across configured, stored and substituted targets.
4. **Fable S1 / S3, R1–R3:** admin TLS, `on_exceeded` validation, and Responses
   denial observability/end-to-end governance tests.
5. **Kimi F7 / F9 and Fable P2:** DCO enforcement for new commits and model-limit
   evidence. Do not rewrite published history or change production model limits
   on the basis of an unverified review claim.
6. **Kimi admin A1 and Fable A3, D1, D3–D5, P1, P3–P6:** control-plane write
   authorization/auditing, panic-path accounting, API-reference completeness,
   profile-qualified claims, release and maintainer processes. Keep these open
   rather than bundling unrelated architecture changes into the accounting fix.

## Verification

The new rejection/flat-cache tests were run before the fix and failed on the
reported defects. After the fix the focused provider tests pass with `-race`,
including explicit zero usage, raw-byte preservation, non-success passthrough,
Converse uncertainty propagation and existing InvokeModel conversion tests.

Full local verification is subject to this environment's socket restrictions:
`httptest` and launcher loopback servers cannot bind. The escalation service
returned `no policy-permitted compatible Responses target`; no restriction was
bypassed. Full race/harness success and Postgres integration coverage must be
established in an authorized environment or CI before merge.
