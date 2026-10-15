# Control-plane policy export / import (backup and restore)

Status: design approved in the 2026-10-15 brainstorming conversation. This spec
is awaiting review, and no implementation is authorized yet.

## Goal

An operator can back up the GovernancePolicy set that `inferplaned` distributes
and later restore it exactly. Restore covers recovery from an operator mistake,
loss of the policy database, or rebuilding a control plane. Both actions are
available through the API, so cron/curl automation works, and through console
buttons.

Today only half of this exists:

- `GET /v1alpha1/config/export` (`internal/controlplane/export.go:21`) renders
  the distributed set as multi-document GovernancePolicy YAML. The console has
  no button for it.
- Import is "deliberately reserved (spec D5): until a UI-editable policy store
  exists" (`export.go:17-19`). That store now exists (ADR-038,
  `internal/policystore`, `PUT`/`DELETE /v1alpha1/policies/{name}`), so the
  reservation is lifted by this spec.
- The existing write path commits one document at a time
  (`Server.ApplyWrite`, `internal/controlplane/policies.go:184`). A
  client-driven sequence of PUT/DELETE calls cannot restore a set atomically.

## Decisions

| # | Decision | Reason |
|---|----------|--------|
| D1 | Scope is the **GovernancePolicy set only**. | See "Out of scope": no other control-plane state may be restored safely. |
| D2 | Import has **replace** semantics. The bundle becomes the complete set, and stored documents absent from the bundle are deleted. | Backup/restore is the use case. Merge can be added later as `mode=merge` without changing the API. |
| D3 | The whole replacement is **one Postgres transaction**. | A partly restored set must never be distributed. |
| D4 | The bundle is **validated in full before any store write**. | Unlike `ApplyWrite`, which commits first and validates the set in `applyWire` afterwards. |
| D5 | Apply requires **`expected_generation`**. A mismatch is a 409. | Stops an import from overwriting an edit made between preview and apply. Reuses `policy.GenerationOf` (`internal/policy/sync.go:147`), so no new versioning is needed. |
| D6 | An empty bundle requires **`allow_empty=true`**. | Replacing with nothing deletes every policy. |
| D7 | Import is **405 without a policy store**. | The file channel is read-only by construction (`ErrNoPolicyStore`), matching PUT/DELETE. |
| D8 | The bundle format is **exactly what export emits**. | Export → import of an unchanged set is a no-op: every document is `unchanged` and the generation stays the same. |

## API

### `GET /v1alpha1/config/export` (unchanged)

The response body is unchanged. The only addition is
`Content-Disposition: attachment; filename="inferplane-policies-<generation-prefix>.yaml"`
so the browser download gets a stable name. Auth stays `authn` (read).

### `POST /v1alpha1/config/import`

- **Auth:** `authnWrite` is the same write authority as policy PUT/DELETE: the
  dedicated `INFERPLANED_POLICY_WRITE_TOKEN` or an OIDC bearer that resolves
  through the group mapping. The heartbeat token is never accepted
  (`policies.go:248-249`).
- **Body:** multi-document GovernancePolicy YAML (YAML or JSON), bounded by
  `importMaxBodyBytes` (64 MiB of raw YAML). This is a memory cap only; an
  oversized body is a 413. The fleet bound is semantic: the parsed set's JSON
  must fit `importMaxSetJSONBytes`, which is 8 MiB, the data plane's
  sync-response decode bound (`internal/proxy/sync.go:377`). A larger set is a
  400 because no data plane could receive it.
  - Block YAML adds bounded per-line indentation relative to JSON. Policy
    nesting is fixed by the schema; a multiline-string fixture measured about
    4.9x.
  - So 64 MiB admits the export of every set within the fleet bound.
  - The 1 MiB `policyMaxBodyBytes` remains the per-document PUT bound.
- **Query:**
  - `dry_run=true` validates and diffs only. It never writes.
  - `expected_generation=<gen>` is required when `dry_run` is not true. A
    missing value is a 400. A value that differs from the current generation is
    a 409.
  - `allow_empty=true` is required to APPLY a bundle with zero documents. A dry run of an empty bundle is allowed and previews every document as `removed`.
- **Response (200, both dry-run and apply):**

```json
{
  "dry_run": true,
  "current_generation": "…",
  "result_generation": "…",
  "changes": [
    {"name": "team-a", "action": "changed"},
    {"name": "team-b", "action": "added"},
    {"name": "legacy", "action": "removed"},
    {"name": "team-c", "action": "unchanged"}
  ],
  "summary": {"added": 1, "changed": 1, "removed": 1, "unchanged": 1}
}
```

The changes are sorted by name. After an apply, `current_generation` is the
generation that was replaced and `result_generation` is the generation now
distributed.

- **Errors:**

| Status | Cause |
|--------|-------|
| 400 | Parse or validation failure (the `ErrPolicyValidation` message is echoed; policies carry no secret values), missing `expected_generation`, or an empty bundle without `allow_empty` |
| 401/403 | Auth (`authnWrite`) |
| 405 | No policy store attached |
| 409 | `expected_generation` mismatch; the response carries `current_generation` |
| 413 | Body over 64 MiB |
| 500 | Store committed but the in-memory install refused the set (`ErrImportNotApplied`; unreachable for a validated bundle). The store holds the NEW set; the response says so and recommends a dry run |
| 503 | Store unreadable during the pre-import reconcile, or `Replace` failed and the read-back does not show the new set. Fixed body `policy store unavailable`. The outcome is not confirmed; re-run a dry run once the store is reachable. A `Replace` error whose read-back shows exactly the predicted set (lost commit acknowledgement) is reported as 200 |

Writes reuse `writeJSONError` / `writePolicyError`.

## Server flow (`Server.ApplyImport`)

1. Hold `s.writeMu` for the whole call, preview and apply alike. If no store
   is attached, return `ErrNoPolicyStore`. Reconcile first: call
   `ReloadFromStore`. A store READ failure (`errStoreList`) is a 503. A stored
   set the loader rejects (an out-of-band DB edit) falls through, because memory
   still holds the last distributed set and the restore exists to overwrite
   those rows. Then read `s.identityConfig`, `s.generation` and `s.wire` under
   `s.mu`. A PUT/DELETE that committed but failed to reload, or a lost commit
   acknowledgement, can therefore never make the precondition judge against
   stale memory.
2. `policy.ParseWireDocs(body)`. Then require `metadata.name` on every
   document, reject duplicate names, and run
   `policy.ValidateIdentitySubjects(docs, identityConfig)`.
3. There is no separate validation pass. `ParseWireDocs` already runs every
   document through `policy.FromV1Alpha1` (`internal/policy/load.go:35`), which
   covers the schema and budget-tier references. With step 2's name, duplicate
   and identity checks, that is every way `applyWire` can reject a set. So
   `applyWire` is NOT refactored, and the post-commit `ReloadFromStore` cannot
   reject what was validated here.
4. Canonicalize each document with `yaml.Marshal` (sigs.k8s.io/yaml, as
   `export.go` and `AttachPolicyStore` do), giving one stored row per document.
   Compute the diff by name against canonicalized current `s.wire`, and compute
   `result_generation = policy.GenerationOf(sorted docs)`.
5. If dry-run, return the diff.
6. Check `expected_generation` against the reconciled `s.generation`. Return
   409 on mismatch. Because of the step-1 `writeMu`, the diff, the check and the
   commit all describe the same set.
7. Call `store.Replace(ctx, docs)`. Then install the already-validated
   reparsed snapshot directly with `applyWire(reparsed, nil)`, setting
   `s.updated` to the commit time. There is NO post-commit DB re-read: a
   failing `List` or a canceled request cannot leave memory on the replaced set
   while the store holds the new one. If the install were ever refused, the
   response is 500 `ErrImportNotApplied` (never the "unchanged" 503), and a
   best-effort `ReloadFromStore` runs on a detached 10s context. Audit entries
   are recorded immediately after a confirmed commit, BEFORE the install: one
   per put/delete plus one `op=import`. A committed restore is therefore never
   unaudited. When `Replace` itself returns an error, a detached 10s read-back
   runs. If the store holds exactly the predicted set, the commit happened (its
   acknowledgement was lost), so the restore is audited and reported as
   success. The order is still "commit, then memory".

Generation consistency: `GenerationOf` is order-sensitive. Step 4 sorts by Go
byte order, and `PostgresStore.List` and authority `readPolicies` both order by
`name COLLATE "C"` (byte order) instead of the server locale. The preview
`result_generation`, the installed generation, a later `ReloadFromStore`, and
the authority's generation check (`internal/authority/pgstore/shared_constraints.go:84`)
therefore all agree. Switching the SQL ordering may change the generation once
on upgrade, when names sort differently under the old locale; data planes just
re-pull the set.

## Store (`internal/policystore`)

Add one interface method:

```go
// Replace atomically makes docs the complete stored set: every existing
// row is deleted and docs inserted in ONE transaction, and the seed
// marker is set so a restored store is never re-seeded from files.
Replace(ctx context.Context, docs []Doc) error
```

`PostgresStore.Replace` is bounded by a 30s `pgWriteTimeout` from entry. It runs
`BEGIN` and then takes `SELECT pg_advisory_xact_lock(847006)` (847005 belongs to
authority/pgstore). This is the
`internal/keystore/postgres_schema.go:69` precedent. It then deletes all rows
from `policies`, inserts each doc with `updated_at = now()`, upserts the seed
marker, and commits. Any error rolls back. The package stays a leaf because it
never parses YAML.

Generation precondition under concurrency: a new `s.writeMu` (a `sync.Mutex`
separate from `s.mu`; lock order `writeMu` before `mu`) is held across the whole store-write-and-install sequence
by `ApplyWrite`, `ApplyDelete` and `ApplyImport`. That makes the step-6
generation check and the step-7 commit/reload atomic against single-document
PUT/DELETE as well as against another import. `s.mu` keeps its existing short
critical sections, so heartbeats are never blocked behind a Postgres round
trip. Cross-replica races are out of scope because the deployment runs a single
policy replica (`docs/runbooks/inferplaned-policy-store.md:23`). The advisory
lock only guarantees that two transactions never interleave rows.

Every `policystore.Store` test double gains `Replace`.

## Audit

Every import emits one `MutationEntry` per document actually added, changed or
removed (`op=put`/`op=delete`). An import `put` carries the `content_sha256`
of the CANONICAL re-marshal that was stored, while an `ApplyWrite` put hashes
the submitted body verbatim. The import also emits one entry with
`op=import`, `name=""`, and the `content_sha256` of the raw submitted bundle.
Unchanged documents emit no entry. The `MutationEntry` field comments document
these meanings. The default `logMutation` sink needs no format change.

## Console (`internal/controlplane/ui/static`)

The Policies view gets two buttons.

- **Export YAML** is always visible when connected. It fetches
  `/v1alpha1/config/export` with the session's read auth and saves the result
  as a Blob download.
- **Import (restore)** is hidden when the deployment has no writable store
  (`policies.writable == false`), and disabled unless `canWrite()` (`app.js:172`).
  A write refusal (403) during preview or apply keeps the read session, as
  PUT/DELETE do (`dashboard.cjs` write-403 contract). Only the export (a read)
  locks on 401/403. The browser rejects files over 64 MiB before uploading. A
  file read that resolves after a disconnect is dropped rather than uploaded
  with the next session's credential. Disconnect disables both buttons.
  1. A file picker accepts `.yaml`, `.yml` or `.json`, read client-side.
  2. A `dry_run=true` POST runs, and the existing policy dialog renders a
     plain-text diff listing (action + name), the summary counts and both
     generations. When `removed > 0`, the confirm button text states the removal
     count ("Restore and delete N policies").
  3. On confirm, the console POSTs with `expected_generation=current_generation`
     from the dry-run response. An empty bundle shows an extra warning and
     sends `allow_empty=true`.
  4. Preview errors (before the dialog opens) appear in the page error banner.
     Apply errors appear in `dialog-error`, and the dialog stays open. A 409
     reads "Policies changed since preview; re-run preview". On success the
     policy list reloads.
  5. The Policies rows of `docs/runbooks/inferplaned-dashboard.md` and its
     `.ko.md` list both controls.

UI strings are English, like the rest of the CP console. That console has no
i18n layer, unlike the `mayu` admin UI's `i18n.js`.

## Out of scope

- **Authority ledger balances** (`internal/authority`): restoring them would
  refund or rewrite central authority. That is forbidden ("expiry never refunds
  central authority"; never "merge/split/refund/rewrite accounts").
- **Usage telemetry:** it is append-only, and a restore would double-count.
  `GET /v1alpha1/usage/export` already covers read-out.
- **Identity bindings:** these are environment configuration
  (`INFERPLANED_IDENTITY_CONFIG`), not store state. Import validates against
  them and never changes them.
- **Merge mode, a CLI subcommand, multi-replica fencing:** possible follow-ups.
- **Data-plane (`mayu`) admin export/import** (`GET /admin/config/export`):
  this is a separate topology channel and is untouched.

## Testing

Go tests use `-race`.

- `internal/controlplane/import_test.go` with an in-memory store:
  - The round trip `export → import dry_run` gives all `unchanged` and equal
    generations.
  - Added, changed and removed are classified correctly.
  - The dry-run `result_generation` equals the post-apply generation.
  - Every validation failure leaves the store untouched and the generation
    unchanged: parse error, missing name, duplicate name, identity-subject
    violation, and a bad budget-tier ref (rejected by `ParseWireDocs`).
  - A missing `expected_generation` gives 400, a stale one gives 409, and the
    store stays untouched.
  - An empty bundle gives 400 without `allow_empty` and empties the set with
    it.
  - No store gives 405. Over 8 MiB gives 413.
  - The heartbeat token is rejected with 403 (`authnWrite`), and the write token is accepted.
  - Mutation entries are emitted for changed documents and the import only.
  - A store `Replace` error leaves memory unchanged, with a 503.
  - A `List` failure after the commit still installs the previewed generation
    (no re-read).
  - An import parked inside `Replace` blocks a concurrent `ApplyWrite` from
    reaching the store, which proves `writeMu`.
- `internal/policystore/postgres_test.go` (existing Postgres test harness):
  - `Replace` swaps the set and sets the seed marker.
  - Injecting a failing insert rolls back the transaction so the prior rows
    survive.
- Console: `internal/controlplane/ui/testdata/policy_import.cjs` in the
  existing cjs harness style covers:
  - the buttons' visibility versus `writable`/`canWrite`;
  - the dry-run diff rendering;
  - the confirm sending `expected_generation`;
  - 409 handling;
  - the empty-bundle warning.

  It is wired into `dashboard_test.go` as the other cjs suites are.

## Docs (same commit as the code, per CLAUDE.md sync rule)

- `internal/controlplane/export.go`: replace the "import is reserved" comment.
- `docs/reference/api.md` + `api.ko.md`: update the "CP config export" row and
  add the import endpoint.
- `docs/runbooks/inferplaned-policy-store.md` + `.ko.md`: add a backup/restore
  section with a cron/curl export example, a dry-run-then-apply restore example,
  and the out-of-scope warning about the ledger and usage.
- `internal/CLAUDE.md` (`policystore/` and `controlplane/` entries): document
  `Replace`, the byte-order listing, and the validate-before-commit import
  ordering.
- `scripts/prepare-policy-controlplane.py` + `tests/deployment/test_policy_store.py`:
  route `/v1alpha1/config/*` to the policy CP origin in the split deployment.
  The runbook covers the manual edit for distributions that were already
  prepared.
- `AGENTS.md`: regenerate only if root `CLAUDE.md` changes (not expected).
