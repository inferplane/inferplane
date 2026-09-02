# Design spec: two-pool user budget (strategy Phase 1)

- Status: **Proposed**, implemented v1 same-day (ADR-046 candidate; the
  ADR-041 propose-then-implement precedent). Composes ADR-042 (per-user
  budget windows) and Phase 0b (durable UserID) — this is why Phase 1
  follows 0b in the delivery sequence.
- Date: 2026-09-02
- Owns: the P0 contract row "Two-pool user budget"
  ([enterprise-strategy.md](../enterprise-strategy.md) §2): *premium pool +
  total hard cap in one explicit window; premium exhausted → first
  compatible model in an admin-approved fallback set; total exhausted →
  deny before egress.*

## 1. Problem

ADR-041's budget-tier substitution is TEAM pressure: one team-wide map,
activated by team utilization, latched per window. What enterprises asked
for per person is a spending ladder: let each developer use premium models
(Opus) until *their own* premium allowance is gone, then serve them an
admin-approved cheaper model automatically, and cut them off entirely at a
personal total cap — without a human in the loop and without the premium
model ever being served past the premium pool.

## 2. Wire schema (v1alpha1, additive)

A USER-subject `budget` rule may carry a `premium` block:

```yaml
spec:
  subject: { user: "https://idp#alice" }   # or a bare sub; team+user also works
  rules:
  - name: pools
    failurePolicy: FailClosed
    budget:
      period: CalendarMonth        # ONE explicit window for both pools
      limitMilliUSD: 200000        # TOTAL pool
      hardCap: true                # total exhausted → deny before egress
      premium:
        limitMilliUSD: 150000      # PREMIUM pool (≤ total)
        models: ["claude-opus-4-8", "claude-opus-*"]  # admin-defined premium set (exact, or one trailing *)
        fallback: ["claude-sonnet-4-6", "glm-4.7-gpu"] # ORDERED admin-approved fallback set
```

Validation (load-time, `UnsupportedError` posture — reject, never ignore):
`premium` requires a user subject and a NUMERIC budget (`unlimited` and
`premium` conflict); `premium.limitMilliUSD` ∈ (0, limit]; `models` and
`fallback` non-empty; fallback entries must not be premium (a fallback
that is itself premium would loop).

## 3. Semantics

- **Accounting:** a request whose SERVED canonical model is in the premium
  set debits BOTH the user's premium counter and total counter; any other
  request debits total only. Both counters live in the rule's one window
  (`budget.ScopeUserPremium` beside the existing user scope, same
  `team + "/" + user` id).
- **Total exhausted** → the existing ADR-042 user-budget 402, before
  egress (`hardCap` semantics unchanged).
- **Premium exhausted, requested model ∈ premium set** → the ingress
  substitutes the FIRST fallback candidate that passes RBAC for this
  principal AND is routed on this data plane — "first compatible". The
  substitution is disclosed exactly like ADR-041
  (`x-inferplane-substituted-model`, audit `model_substituted_from`).
- **No compatible fallback** → 402 `user premium budget exhausted` — the
  premium model is NEVER served past the pool (the contract's "blocks,
  never serves the premium model"; this is the one deliberate difference
  from `SubstituteTier`, whose never-deny contract fits team pressure but
  not a personal ceiling — hence a separate router seam).
- Ordering at ingress: model_fallbacks → ADR-041 team tier → **user pool**
  → final RBAC. The user pool runs last so a team-tier substitution's
  target is itself pool-checked, and its output re-passes `Allows`.
- **Per-plane accuracy caveat** (inherited from ADR-042 Phase 3,
  documented): user counters are per-data-plane; with N planes a user's
  effective pools are up to N× until user-keyed leases exist (roadmap ②'s
  windowID work is the prerequisite).

## 4. Implementation map (v1, shipped with this spec)

| Layer | Change |
|---|---|
| `api/v1alpha1` | `BudgetRule.Premium *PremiumPool{LimitMilliUSD, Models, Fallback}` |
| `internal/policy` | `Budget.Premium*` fields; validation above; `UserLimits` carries the premium config — when several rules define premium for one user, the LOWEST premium limit's rule wins wholesale (most-restrictive, deterministic tie by policy/rule name) |
| `internal/governance` | `UserPolicy.Premium*`; `Settle` debits `ScopeUserPremium` when the served model is premium; `PremiumExhausted(subject)` read for the gate; `UsageOf` gains `user_premium` (appended, omitempty) |
| `internal/router` | `SetUserPoolGate` + `ApplyUserPool(p, model) (served string, substituted, blocked bool)` — first compatible fallback or blocked |
| `cmd/mayu/gateway.go` | gate closure: policy lookup (canonical + bare-sub, the 0b-2 rule) + `gov.PremiumExhausted` |
| ingresses ×3 | one `ApplyUserPool` call after the ADR-041 seam; blocked → 402 |

## 5. Acceptance (strategy §5 Budget rows covered)

- premium exhaustion selects an approved compatible target (e2e);
- no compatible fallback blocks — never serves the premium model (e2e);
- total exhaustion issues no provider request (existing ADR-042 e2e + new);
- non-premium traffic never debits the premium pool (unit).

Open (recorded): concurrent near-cap reservation (needs the Phase 1 atomic
reserve/settle ledger — the PreCheck estimate charge covers the common
case today), quota fallback-or-block wording, user-keyed leases.
