# Verified identity and stable account references

Status: **opt-in mechanism implemented; alpha**. This guide describes the identity
implementation in this branch, not activation on a running deployment. Six-role
org/team authorization, the complete premium/total pool contract, and operational
qualification remain open.

Identity binds credentials to `(organization, kind, issuer, subject)`. Human
issuer/subject come from verified OIDC claims; JSON, userinfo and `owner` are not
identity proof. Service issuers are derived as
`inferplane://<organization>/service-account`. One immutable organization is
supported per store/authority namespace. Preserve exact issuer/subject strings;
do not normalize or guess them. SQLite and Postgres persist the registry.
Enabling identity does not change the [deployment profile's](../README.md#deployment-profiles)
rate, money or availability guarantees.

## Configuration

| Setting | Behavior |
|---|---|
| No identity declaration on a never-required store | Existing legacy owner attribution; verified identity is not enforced by default. |
| `key_store.identity` with `required: false` | Optional typed attribution preserves legacy accounting references. This is not required enforcement or a dry run: declaration/registry metadata can be persisted. |
| `key_store.identity` with `required: true` | Require valid registry-bound identity; reject incompatible existing keys, declarations and old writes. |

[declaration.json](../examples/identity/declaration.json) and
[keystore.json](../examples/identity/keystore.json) are **fresh-store examples**.
Their empty bindings are not a migration declaration for populated stores.
Replace the organization, path and reviewed bindings before use. The keystore
example is sufficient for the privileged key CLI, not a complete gateway config.

In an existing gateway config, place the declaration object directly under
`key_store.identity`; this field is an object, not a filename. Keep the selected
SQLite/Postgres backend and all existing provider/policy settings. Required mode
with a control plane also requires `control_plane.require_sync: true`.
Use HTTPS for remote control-plane transport (loopback HTTP is allowed for local
development) and a referenced, nonempty non-JWT machine token.

On `inferplaned`, set `INFERPLANED_IDENTITY_CONFIG` to the path of a JSON file
containing the **declaration object itself**, such as `declaration.json`, not the
`key_store` wrapper. It does not replace machine credentials, policy storage or
ADR-045/046 configuration. Required CP deployments and participating data planes must use the same
validated declaration. Binding order/JSON formatting do not matter to its
fingerprint; organization, required mode and bindings do. Dynamic enrollment of
new canonical identities does not change the declaration fingerprint.

Required synchronization rejects missing, old or mismatched identity fingerprints
before policy/authority installation. A detected incompatibility gates governed
generation; count APIs remain local/200. Fingerprint equality is an
interoperability check, not machine authentication or protection from a compromised
node.

## Preserve existing accounts before requiring identity

There is no automatic migration/merge command. Configuration installs registry
bindings and identity evidence transactionally; it does not rewrite financial
accounts, windows, grants or permits.

1. **Inventory all historical nonempty legacy owners, including revoked keys.**
   Reviewing only active credentials is insufficient: revoked-only owners may
   still identify liabilities. Every such owner needs a trusted matching registry
   binding. Use explicit declaration bindings for legacy owners; already enrolled
   canonical identities must remain consistent with their existing registry entry.
2. Establish the exact verified issuer/subject and organization from trusted
   evidence. Never infer the issuer from email, owner spelling or
   `metadata.source`. An ambiguous owner shared by different people is a reason
   to stop, not split an account or guess which person owned the spend.
3. **Revoke active empty-owner keys and reissue verified credentials** through
   the reviewed rollout. An empty reference is not a valid binding. Revocation
   does not refund spending or discard outstanding liabilities; retain historical
   rows and revocation tombstones.
4. Prepare a one-to-one declaration. For example, replace this illustrative
   entry with an operator-verified mapping:

   ```json
   {
     "identity": {
       "organization": "example-org",
       "kind": "human",
       "issuer": "https://idp.example.invalid",
       "subject": "verified-opaque-subject"
     },
     "account_ref": "legacy-owner-001"
   }
   ```

   Place it in `bindings` on both sides. Existing nonempty typed evidence must
   agree. Multiple identities cannot share an account, and an identity cannot
   acquire another reference. No account merges, splits or implicit refunds are
   supported.
5. Before cutover, stop old writers/new admission as required by the rollout,
   upgrade/fence participating processes and reconcile the inventory of open
   authority. Keep unavailable/unresolved credit unavailable. Configure matching
   stores and CP/data-plane declarations, then verify readiness and issuance.
   Required activation refuses unmapped history or inconsistent keys; do not
   bypass this by deleting rows or weakening required mode.

Declaration/backend changes require restart. Once required mode is persisted,
missing, disabled or conflicting declarations are refused; complete legacy
bindings before activation. A later declaration change needs a separately
reviewed migration, not a hot reload or ad hoc database edit.

## Use the account reference, not an audit alias

New identities without a legacy binding receive an `identity-v1:<SHA-256>`
canonical account reference. An explicitly legacy-bound person keeps the exact
legacy `account_ref` for policy, usage and financial lookup.

For the example above, `subject.user` remains **`legacy-owner-001`**. Do **not**
replace it with that person's canonical `identity-v1:…` reference: required policy
validation rejects that alias because it would address a different account.
The canonical reference still appears in identity audit evidence for correlation.
Use the authorized key view's `account_ref` for accounting selectors and its
digest-only `identity.reference` for identity evidence.

Key rotation and a second device retain the registered account. Historical
account/window keys and late settlements remain unchanged. Optional attribution
does not silently substitute a new financial reference.

## Issuing credentials

- **Humans:** use configured OIDC login (`mayu login`; see
  [CLI login](runbooks/cli-login.md)). Minting uses verified issuer/subject and the
  store organization, with issuer-qualified throttling. Required mode leaves
  Owner resolution to the registry; caller-supplied tuples cannot impersonate a
  human or select service kind.
- **Admin API:** only a full admin can supply `account_ref` to select an already
  registered identity, or `service_account` to create a stable service identity.
  These inputs are mutually exclusive. A team-entitled OIDC user cannot use
  either to escape a user cap; self issuance derives their own verified Human
  identity. Required-mode owner-only issuance is refused.
- **Service CLI:** after the reviewed configuration is installed, a privileged
  store administrator can run:

  ```bash
  mayu keys create --config /etc/inferplane/config.json \
    --team engineering --service-account ci-runner
  ```

  Use the same service ID on rotation. Direct keystore/DSN access is privileged
  administration, not a team-user authorization path. Human creation uses OIDC,
  not arbitrary CLI issuer/subject flags. The CLI reads key-store configuration
  without resolving unrelated provider/CP secrets.

Audit appends optional canonical-reference/kind/organization and issuer/subject
digests, preserving old record bytes. New identity DTOs contain no raw issuer or
subject; no identity/account labels are added to metrics. Authorized account
references can still retain historical owner bytes. These are identifiers, so
protect declaration files, registry access and support exports accordingly.

## Failure, restore and qualification

Identity does not remove existing failure gates: initial required sync,
`max_policy_age`, hard grant/window expiry and exhaustion still apply. A detected
identity mismatch cannot use valid credit to bypass refusal. Ordinary CP
unreachability after valid sync is different from an explicit incompatible reply:
ADR-045 may use existing credit only within all gates; ADR-046 still needs its DB
for new key resolution/admission and fails closed on DB loss.

Restoring an old registry/DB is not a refund or a safe rollback by itself. Restore
identity, policy and authority state consistently, fence writers and account for
all already-issued grants before exposing restored state. A DB restore cannot
retroactively revoke offline authority. Do not reopen a stale snapshot when newer
outstanding grants cannot be accounted for; unreachable nodes or expired grants
do not prove unused credit. Preserve unresolved liabilities. No automatic recovery
or account rewrite is provided by identity. Brokering/fingerprints do not make a
compromised laptop bypass-proof.

Before production activation, validate rotation across devices/issuers, immutable
legacy references (including revoked-only history), refusal of stale/unmapped
keys and declarations, DB outage/restore procedures, and the chosen profile's
capacity. Source changes and local tests are not a deployed-fleet qualification.
