# ADR-048: Verified identities with stable accounting references

Status: Accepted implementation design, 2026-09-16.
Extends ADR-028/042/045/046. Does not complete management-role, user-pool or
production-qualification contracts.

## Decision

An opt-in identity declaration binds a credential to
`(organization, kind, issuer, subject)` in a persistent SQLite/Postgres registry.
Human tuples come from verified OIDC claims; service issuers are server-derived
and service IDs survive key rotation. Each store/authority namespace has one
immutable organization.

The identity tuple and its `account_ref` form a bijection. New identities receive
a versioned, domain-separated SHA-256 reference. An explicitly mapped legacy
identity retains its exact old accounting reference. `keys.owner` remains a
compatibility mirror of this reference in required mode, validated by the
registry; it is not freely editable authority.

Do not change policy/rule/subject/period accounting hashes, window IDs, grants,
permits, rate segments or terminal replay identities during binding. Substituting
a new canonical reference for a mapped old reference could resurrect allowance,
so required policy validation rejects that substitution.

## Migration and storage

`key_store.identity` may initially store optional typed attribution while
preserving existing owner semantics. Required activation validates explicit
bindings against all historical nonempty owners, including revoked credentials.
Active empty-owner keys must be revoked/reissued. Missing or contradictory
historical identity requires operator-supplied trusted evidence; owner strings,
email and `metadata.source` do not establish the missing issuer.

Configuration installs bindings transactionally. Bindings cannot be reassigned,
merged or split. Required declarations cannot be disabled or silently changed.
Canonical enrollment remains possible without changing the declaration
fingerprint. Imports preserve registry data, original references and revocation
history, or refuse conflicts atomically. Original bootstrap fingerprints remain
compatible and do not overwrite administrator changes.

Database guards constrain old inserts, identity/owner updates and reactivation
paths, including SQLite replacement statements with foreign keys disabled.
Financial admission has transaction-local identity checks and write guards:
old software cannot create fresh grants/permits after activation simply by
omitting identity metadata. Existing terminal settlement retains its original
capability and booking references. No expiry/disappearance refund is introduced.

## Policy distribution

`inferplaned` reads `INFERPLANED_IDENTITY_CONFIG`; participating data planes use
the same declaration. Required remote synchronization uses HTTPS and a referenced
non-JWT machine credential; loopback HTTP is allowed for local tests/development.
Fingerprint equality asserts compatible declarations, not authentication.

Sync and durable-authority envelopes carry an identity fingerprint. A required
client validates it before installing authority or publishing policy. A rejected
policy cannot advance the acknowledged generation or clear readiness on an
unchanged, body-less heartbeat. Recovery requires a complete, content-hash-matched
snapshot. `identityPoliciesComplete` explicitly identifies a full empty snapshot
where the legacy `policies,omitempty` field would otherwise be ambiguous.

Count endpoints remain local/200 during generation refusal. Identity declarations
and backend changes require restart; there is no mode downgrade through hot reload.
Publishing a full required-policy snapshot briefly gates new generation with 503,
including same-generation full durable-authority heartbeats. Existing admitted
attempts keep their original accounting. This conservative publication boundary
must remain explicit in retry/latency qualification; it is not zero-downtime
admission during every update.

## Evidence and scope

Audit appends digest-only identity evidence without rewriting historical lines.
Usage attribution uses the canonical identity reference, while financial lookup
retains the registered accounting reference. No identity/account metric labels
are added. Full-admin identity selection/service creation and self OIDC minting
remain distinct; team-entitled callers cannot escape their cap by inventing a
service identity.

This mechanism assumes trusted enforcement processes and database administration.
It does not make a compromised developer host bypass-proof. Rollout requires
upgrading/stopping old writers and preserving outstanding authority; already-issued
offline grants cannot be revoked retroactively. Restoring stale databases still
requires reconstruction or conservative retention of post-backup liabilities.

Six-role authorization, the complete premium/total pool product contract, account
merges, reconciliation APIs and deployed failover/load qualification remain open.
The three deployment profiles retain their distinct availability and enforcement
limits.

See [operator guide](../verified-identity.md) and
[implementation design](../superpowers/specs/2026-09-16-verified-identity.md).
