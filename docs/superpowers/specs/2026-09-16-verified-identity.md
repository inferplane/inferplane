# Verified identity with stable accounting references

Status: implementation design, 2026-09-16. Baseline: `698edf3`.

## Contract

Bind credentials to one organization and a typed verified issuer/subject identity.
Keep existing accounting references unchanged when an operator explicitly binds a
legacy identity. The registry is a bijection between identity tuples and account
references, not an account-merging service. No identity operation refunds,
renames, truncates or copies an authority account, grant, permit or window.

Implement this before six-role authorization, which remains separate work.
Managed identity is opt-in and must not be advertised as enabled in default
legacy configurations or deployed merely because its tests pass.

## Leaf representation

`internal/identity` has no server/config/store imports:

```go
type Kind string // Human = "human", Service = "service"
type ID struct {
    Organization string `json:"organization"`
    Kind Kind `json:"kind"`
    Issuer string `json:"issuer"`
    Subject string `json:"subject"`
}
type Binding struct {
    Identity ID `json:"identity"`
    AccountRef string `json:"account_ref"`
}
type Config struct {
    Organization string `json:"organization"`
    Required bool `json:"required"`
    Bindings []Binding `json:"bindings,omitempty"`
}
func NewHuman(org, verifiedIssuer, subject string) (ID, error)
func NewService(org, serviceID string) (ID, error)
func (id ID) Validate() error
func (id ID) CanonicalRef() string
func (id ID) Audit() AuditRef
func (b Binding) Validate() error
func (c Config) Validate() error
func (c Config) Fingerprint() string
```

Reject empty, control-character, invalid-UTF8 and excessive-length fields.
Human issuers are absolute HTTPS URLs without userinfo/query/fragment.
Do not normalize issuer or subject strings. Service issuer is derived as
`inferplane://<organization>/service-account`; service ID survives key rotation.
Constructors validate syntax; only verified OIDC claims establish human trust.

Canonical references use `identity-v1:` plus full SHA-256 over the JSON array
`[organization, kind, issuer, subject]`. Include a fixed version/domain prefix
in the hashed representation. Exact identity equality uses all four fields.
Legacy account references retain their exact bytes, bounded to the existing
256-byte owner limit and excluding control characters. Different identities
cannot share a reference, including a legacy/canonical collision.

`AuditRef` contains the canonical identity reference, kind, organization and
issuer/subject digests. It contains neither raw issuer nor raw subject; metrics
must never gain person/reference labels.

Fingerprint a validated, sorted binding declaration plus organization and required
mode. Dynamically enrolled canonical identities do not change this declaration
fingerprint. A fingerprint is an interoperability assertion, not authentication.

## Persistent registry and keys

Implement the same logical schema in SQLite and Postgres:

```text
identity_registry(
  organization, kind, issuer, subject, account_ref,
  PRIMARY KEY(organization, kind, issuer, subject),
  UNIQUE(organization, account_ref))
identity_mode(singleton, organization, required, fingerprint)
keys: append identity_organization, identity_kind, identity_issuer,
             identity_subject (TEXT, default empty)
```

Exactly one immutable organization is supported per store/authority namespace.
An absent configuration retains legacy behavior only if the store has never
activated required identity. A required store cannot be reopened with a missing,
disabled or conflicting declaration.

Add `Identity *identity.ID` to `keystore.KeyOptions` with `omitempty`.
In required mode, `Owner` is a compatibility mirror of the immutable registry
`account_ref`, not caller-selected attribution. New code obtains its user scope
through `Principal.AccountSubject()`; this method returns the validated account
reference for bound keys and the old owner only for explicitly legacy keys.

Expose optional `keystore.IdentityStore`:

```go
type IdentityStore interface {
    ConfigureIdentity(context.Context, identity.Config) error
    IdentityConfig(context.Context) (identity.Config, error)
    ResolveIdentity(context.Context, identity.ID) (identity.Binding, bool, error)
    IdentityByRef(context.Context, string) (identity.Binding, bool, error)
}
```

CreateWithOptions/EnsureKey handle credential persistence; no public API may
invent a human tuple from an inference header or free-form owner.
When Required is false, verified identity can be stored as dormant attribution
without changing the old owner/account reference. No new financial reference is
silently substituted during this stage.

In required mode a new validated identity receives its existing registry
reference, or its canonical reference if no binding exists. Caller-supplied
nonempty Owner must match that reference; mismatches refuse. Enrollment and
credential insertion are atomic. Registry bindings are immutable.

ConfigureIdentity is transactional. Explicit Bindings can attach known old
references to identities and annotate corresponding legacy keys; existing
nonempty typed evidence must agree. Validate the whole batch first and roll back
on conflict. Never infer historical issuer from owner/email/metadata.source.
Required activation refuses any unbound active key or conflicting mapping.
Historical nonempty owners also require bindings, including revoked/expired keys;
empty-owner revoked history does not invent a person. Revoked keys remain revoked
and are preserved in imports, with monotonic revocation guards.

Install SQLite and Postgres write guards enforcing registry membership and
owner/tuple consistency in required mode. SQLite guards must work with foreign
keys disabled and must reject old INSERT/UPDATE/reactivation statements. Freeze
old bootstrap fingerprint serialization; typed identity changes require explicit
consistency checks without overwriting original fingerprints or revocations.

Raw SQLite-to-Postgres import carries typed columns, registry entries, mode and
revoked rows transactionally. Different organizations/declarations or conflicting
bindings refuse. Import retry remains idempotent. No account merges/splits are
supported.

## Configuration and trust

Expose the configuration as `key_store.identity`. The limited key CLI loader must
read it without resolving provider/control-plane secrets. Gateway and CLI store
opening configure/check persistent identity state.

Required managed mode with a control plane requires `require_sync: true`,
HTTPS (or loopback HTTP) and a referenced nonempty non-JWT machine token.
Control plane configuration uses `INFERPLANED_IDENTITY_CONFIG`, a JSON file with
the same identity.Config declaration. Required CP mode rejects old/missing or
different identity fingerprints before grants/policy delivery. SyncResponse
echoes the required fingerprint; mayu validates it before installing authority or
publishing policy. Missing/mismatched identity protection immediately gates
governed generation; count APIs stay local/200.

Validate user selectors: `subject.user` continues to reference an account ID.
Legacy references must appear in the explicit binding declaration. Canonical
versioned references are valid selector syntax; credential registry resolution
establishes the matching person's identity. No typed selector syntax is added.
Do not modify AuthorityBudgets account/window derivation or terminal settlement.

Shared key snapshots include typed evidence and required-mode identity state.
Shared admission revalidates that same evidence and declaration, and rejects stale
authorization/policy identity. Local registries must install the declaration
before publishing a matching required CP policy. Machines remain trusted
enforcement components; fingerprints do not make a compromised node trustworthy.

Identity declaration/backend changes require restart; no downgrade through hot
reload. Before production activation, operators stop old writers, upgrade/fence
participating nodes and preserve old open authority as liability. Offline grants
cannot be retroactively revoked; expired/vanished nodes never justify refunds.
Code must refuse incompatible clients and unsafe mappings, not pretend this
rollout qualification has been performed.

## Issuance, policy and audit

`adminauth.Claims` and `principal.AdminIdentity` retain verified issuer.
OIDC minting constructs the human ID from the verifier's issuer and subject
plus the configured organization. Client JSON cannot override them. Mint throttles
use the issuer-qualified identity rather than bare subject in managed mode.

Admin key creation may select an already enrolled identity through an explicit
account reference only with full-admin authority; team-entitled users cannot
impersonate another identity. Service creation uses server-derived issuer and a
stable service ID. Existing implicit owner-only issuance refuses in required mode.
Add a service-ID option to the keys CLI; do not derive identity from generated
key IDs. Key views return typed identity/reference to authorized administrators.

All inference ingress, policy gates, shared/local reservations, telemetry and audit
use the validated account subject. Preserve raw legacy reference bytes for
financial lookup, but use the canonical reference/digests for new identity
observability. Append optional audit identity evidence so historical records
serialize unchanged; include mixed-version verification fixtures.

## Acceptance

- One verified person on two devices/rotated keys resolves one account.
- Equal subjects from different HTTPS issuers remain different people.
- Service identity survives rotation and cannot impersonate human identity.
- Legacy binding keeps account hashes, window IDs, money liabilities, rate debt,
  pending permits, late terminal outcomes and replay semantics unchanged.
- Conflicting mappings, unbound active keys, stale mode/authorization, failed
  registry reads and old writes fail closed.
- Existing seed fingerprints and revoked import rows survive upgrade/retry.
- No authority-table rewrite/refund, user metric label, secret exposure or raw
  issuer/subject audit fields are introduced.
- Both static builds, all race/vet/harness tests, actual Postgres integration,
  seven-test CI DB gate and latest-HEAD review must pass.
