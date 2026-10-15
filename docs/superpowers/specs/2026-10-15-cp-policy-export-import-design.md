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
  `policyMaxBodyBytes` (1 MiB). An oversized body is a 413.
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
| 413 | Body over 1 MiB |
| 503 | Store failure (`writePolicyError` default, fixed body `policy store unavailable`); the store and the in-memory set are left unchanged |

Writes reuse `writeJSONError` / `writePolicyError`.

## Server flow (`Server.ApplyImport`)

1. Read `s.policyStore`, `s.identityConfig`, `s.generation` and `s.wire` under
   `s.mu`. If there is no store, return `ErrNoPolicyStore`.
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
6. Check `expected_generation` against `s.generation`. Return 409 on mismatch.
   For a non-dry-run request, `ApplyImport` acquires `s.writeMu` (see Store)
   before step 1 and holds it through step 7. The diff, the check and the commit
   therefore all describe the same set.
7. `store.Replace(ctx, docs)`, then `recordMutation` for each put/delete plus
   one `op=import` entry, then `ReloadFromStore(ctx)`. The order is "commit,
   then memory", the same as `ApplyWrite`. Because validation already ran,
   reload cannot reject the committed set.

Generation consistency: `ReloadFromStore` lists rows ordered by name, and
`GenerationOf` is order-sensitive. Step 4 sorts by name for the same reason,
so the dry-run `result_generation` equals the generation installed after apply.

## Store (`internal/policystore`)

Add one interface method:

```go
// Replace atomically makes docs the complete stored set: every existing
// row is deleted and docs inserted in ONE transaction, and the seed
// marker is set so a restored store is never re-seeded from files.
Replace(ctx context.Context, docs []Doc) error
```

`PostgresStore.Replace` runs `BEGIN` and then takes
`SELECT pg_advisory_xact_lock($1)` with a package constant. This is the
`internal/keystore/postgres_schema.go:69` precedent. It then deletes all rows
from `policies`, inserts each doc with `updated_at = now()`, upserts the seed
marker, and commits. Any error rolls back. The package stays a leaf because it
never parses YAML.

Generation precondition under concurrency: a new `s.writeMu` (a `sync.Mutex`
separate from `s.mu`) is held across the whole store-write-and-reload sequence
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
removed (`op=put`/`op=delete`, with `content_sha256` for puts). It also emits
one entry with `op=import`, `name=""`, and `content_sha256` of the raw
submitted bundle. Unchanged documents emit no entry. The default `logMutation`
sink needs no format change.

## Console (`internal/controlplane/ui/static`)

The Policies view gets two buttons.

- **Export YAML** is always visible when connected. It fetches
  `/v1alpha1/config/export` with the session's read auth and saves the result
  as a Blob download.
- **Import (restore)** is visible only when `canWrite()` (`app.js:172`).
  1. A file picker accepts `.yaml`, `.yml` or `.json`, read client-side.
  2. A `dry_run=true` POST runs, and the existing policy dialog renders a diff
     table (name + action), the summary counts and both generations. Removed
     documents are highlighted. When `removed > 0`, the confirm button text
     states the removal count.
  3. On confirm, the console POSTs with `expected_generation=current_generation`
     from the dry-run response. An empty bundle shows an extra warning and
     sends `allow_empty=true`.
  4. A 409 shows "Policies changed since preview; re-run preview". 400 and 413
     show the server message in `dialog-error`. On success the policy list
     reloads.

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
  - No store gives 405. Over 1 MiB gives 413.
  - The heartbeat token is rejected with 403 (`authnWrite`), and the write token is accepted.
  - Mutation entries are emitted for changed documents and the import only.
  - A store `Replace` error leaves memory unchanged, with a 503.
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
  `Replace` and the validate-before-commit import ordering.
- `AGENTS.md`: regenerate only if root `CLAUDE.md` changes (not expected).
