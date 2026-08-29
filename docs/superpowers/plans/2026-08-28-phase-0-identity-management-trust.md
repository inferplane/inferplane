# Phase 0 Identity and Management Trust Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every credential a durable typed identity, enforce fixed role/capability grants with organization and team scope on every management endpoint, and emit reproducible mutation audit evidence.

**Architecture:** Add two leaf packages: `internal/identity` owns comparable human/service identities, while `internal/authz` owns fixed roles, capabilities, scopes, bindings, and authorization decisions. OIDC and virtual-key code resolve into those types; management HTTP layers authenticate first and then authorize through one route matrix. `mayu` uses a local SQLite binding store and its existing audit writer; `inferplaned` separates machine and management credentials, uses a Postgres binding store when mutable authorization is enabled, and gains a management audit writer.

**Tech Stack:** Go 1.25, `coreos/go-oidc`, `modernc.org/sqlite`, `pgx/v5`, `net/http` Go 1.22 patterns, existing `internal/audit` hash chain, CRD-style `api/v1alpha1` policy schema.

## Global Constraints

- The durable human ID is exactly `(OIDC issuer, OIDC subject)`; email, key ID, and free-form owner text are never authorization or policy keys.
- Service accounts are explicitly `kind=service` and use the server-derived issuer `inferplane://<organization>/service-account` plus a required subject.
- `Config.Organization` is the one non-empty organization for a standalone `mayu`; every SQLite key store opens with it, legacy rows migrate into it, and a store file cannot later be opened under a different organization.
- Fixed roles are `platform-admin`, `policy-admin`, `provider-admin`, `budget-admin`, `auditor`, and `team-admin`; no custom-role language is introduced.
- Every authorization grant has exactly one organization and either organization-wide scope or a non-empty team set.
- Static break-glass credentials resolve only to `platform-admin` at organization scope; they never become machine-channel credentials.
- `POST /v1alpha1/sync`, `POST /v1alpha1/usage`, and `POST /v1alpha1/credentials` remain machine channels and never accept OIDC management credentials.
- Policy and authorization can only narrow existing access; migration of legacy `subject.user` is rejected explicitly rather than silently broadening to team-only.
- Audit records contain stable issuer digest + opaque subject, never email, raw groups, bearer tokens, prompt text, or secret values.
- Existing audit struct fields stay byte-compatible; new optional fields are appended so mixed-version hash chains still verify.
- Phase 0 does not implement user budget pools, reservation ledgers, PII routing, cache-point translation, or global rate shares; it only provides the identity and authorization keys those phases consume.
- Do not modify or revert unrelated working-tree changes. Do not create an ADR unless the user asks for one.

## File Structure

### New files

- `internal/identity/identity.go` — typed, validated, comparable human/service identity and safe issuer digest.
- `internal/identity/identity_test.go` — identity validation, service issuer derivation, and collision tests.
- `internal/authz/model.go` — fixed roles, capabilities, scopes, bindings, and role capability table.
- `internal/authz/authorize.go` — resolver and fail-closed authorization decisions.
- `internal/authz/store.go` — binding-store interface and validation.
- `internal/authz/sqlite.go` — standalone `mayu` binding persistence.
- `internal/authz/postgres.go` — central `inferplaned` binding persistence and migration lock.
- `internal/authz/*_test.go` — table, resolver, scope, migration, and concurrent-store tests.
- `internal/server/authz.go` — reusable management authorization middleware and denial audit adapter for `mayu`.
- `internal/server/adminapi/rolebindings.go` — scoped role-binding CRUD for standalone management.
- `internal/controlplane/managementauth.go` — management-only authentication and capability middleware.
- `internal/controlplane/rolebindings.go` — central role-binding CRUD.
- `internal/audit/mutation.go` — canonical SHA-256 digests and mutation-record constructor.
- `internal/audit/mutation_test.go` — deterministic digest and optional-field compatibility tests.
- `cmd/mayu/identity_e2e_test.go` — OIDC mint → key rotation → same durable identity flow.
- `cmd/inferplaned/authz_e2e_test.go` — machine/management channel separation and role matrix.

### Modified files

- `internal/adminauth/oidc.go` and tests — include verified issuer in claims.
- `internal/principal/principal.go` and tests — replace `Subject/Teams/IsAdmin` authorization state with typed identity and grants.
- `internal/keystore/keystore.go`, `sqlite.go`, tests — persist organization and typed identity on every key.
- `internal/server/authapi/authapi.go`, `adminapi/keys.go`, `adminapi/teams.go`, `adminapi/whoami.go` and tests — mint typed identities, scope list/read/write operations, and remove owner from authority decisions.
- `api/v1alpha1/types.go`, CRD, policy conversion/store/tests — replace free-form user selector with typed identity selector.
- `cmd/mayu/gateway.go` and tests — assemble resolver/store, feed typed identity to policy, and audit pricing reload changes.
- `internal/server/server.go` and tests — mount every management route with an explicit capability.
- `internal/server/{anthropicapi,openaiapi,bedrockapi}` — propagate stable identity into usage and audit records.
- `internal/telemetry/{usage,collector}.go` and tests — group usage by a stable serialized identity reference, not owner.
- `internal/audit/record.go` and tests — append identity and mutation evidence fields.
- `internal/config/config.go` and tests — replace admin/team group mappings with authorization bootstrap bindings.
- `internal/controlplane/{auth,controlplane,usage,policies,export}.go` and tests — separate machine and management authentication and add capability gates.
- `cmd/inferplaned/{main,oidcenv}.go` and tests — configure organization, management break-glass, authorization store, and audit writer.
- `internal/server/adminui/static/{app.js,i18n.js}` and `internal/controlplane/ui/static/app.js` — hide unauthorized actions using the server-returned capability set; server checks remain authoritative.
- `README.md`, `docs/architecture.md`, `docs/onboarding.md`, `docs/api-reference.md`, `docs/reference/{api,data,security,infrastructure}.md`, `docs/roadmap.md`, `docs/enterprise-strategy.md`, root/module `CLAUDE.md` — document the implemented contract and leave later phases explicitly open.

---

### Task 1: Add the durable typed identity leaf

**Files:**
- Create: `internal/identity/identity.go`
- Create: `internal/identity/identity_test.go`

**Interfaces:**
- Produces: `identity.ID`, `identity.Kind`, `identity.NewHuman`, `identity.NewService`, `ID.Validate`, `ID.IssuerDigest`, and `ID.Equal`.
- Consumes: standard library only.

- [ ] **Step 1: Write failing identity tests**

```go
func TestHumanIdentityUsesIssuerAndSubject(t *testing.T) {
	id, err := NewHuman("https://idp.example.com/pool", "00u-123")
	if err != nil { t.Fatal(err) }
	if id.Kind != Human || id.Issuer != "https://idp.example.com/pool" || id.Subject != "00u-123" {
		t.Fatalf("unexpected id: %+v", id)
	}
	other, _ := NewHuman("https://other.example.com/pool", "00u-123")
	if id.Equal(other) { t.Fatal("same sub at different issuers must not collide") }
}

func TestServiceIdentityIssuerIsServerDerived(t *testing.T) {
	id, err := NewService("acme", "build-bot")
	if err != nil { t.Fatal(err) }
	if id.Issuer != "inferplane://acme/service-account" { t.Fatalf("issuer=%q", id.Issuer) }
}

func TestIdentityRejectsEmptyAndControlCharacters(t *testing.T) {
	for _, id := range []ID{{Kind: Human}, {Kind: Service, Issuer: "x", Subject: "a\nb"}} {
		if err := id.Validate(); err == nil { t.Fatalf("accepted %+v", id) }
	}
}
```

- [ ] **Step 2: Run the package test and verify red**

Run: `go test ./internal/identity -run 'Test(Human|Service|Identity)' -v`

Expected: FAIL because the package and symbols do not exist.

- [ ] **Step 3: Implement the minimal leaf type**

```go
package identity

type Kind string

const (
	Human   Kind = "human"
	Service Kind = "service"
)

type ID struct {
	Kind    Kind   `json:"kind"`
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

func NewHuman(issuer, subject string) (ID, error)
func NewService(organization, subject string) (ID, error)
func (id ID) Validate() error
func (id ID) Equal(other ID) bool
func (id ID) IssuerDigest() string // "sha256:" + lowercase hex SHA-256
```

Validate exact non-empty strings, reject control characters and values over 2048 bytes, require an absolute HTTPS issuer for `Human`, and derive rather than accept the service issuer.

- [ ] **Step 4: Run focused tests and package hygiene**

Run: `go test ./internal/identity -race && go vet ./internal/identity && test -z "$(gofmt -l internal/identity)"`

Expected: PASS with no output from vet/gofmt.

- [ ] **Step 5: Commit the identity leaf**

```bash
git add internal/identity/identity.go internal/identity/identity_test.go
git commit -s -m "feat(identity): add durable typed subjects"
```

---

### Task 2: Carry the verified OIDC issuer in authentication claims

**Files:**
- Modify: `internal/adminauth/oidc.go:20-35,105-154`
- Modify: `internal/adminauth/oidc_test.go`
- Modify: claim fixtures in `internal/server/adminauth_test.go`, `internal/controlplane/auth_test.go`, `cmd/mayu/*_test.go`, and `cmd/inferplaned/*_test.go`

**Interfaces:**
- Consumes: `identity.ID` from Task 1.
- Produces: `adminauth.Claims{Identity identity.ID, Groups []string}`. Authorization principals and grants are deliberately unchanged until Task 5 creates `internal/authz`.

- [ ] **Step 1: Write the failing issuer-propagation test**

```go
func TestVerifyReturnsIssuerBoundIdentity(t *testing.T) {
	claims := verifyTestToken(t, "user-1", []string{"ops"})
	if claims.Identity.Issuer != idp.srv.URL || claims.Identity.Subject != "user-1" {
		t.Fatalf("identity=%+v", claims.Identity)
	}
}
```

Add a second assertion proving the same subject from another verified issuer produces a different `identity.ID`.

- [ ] **Step 2: Run the focused test and verify red**

Run: `go test ./internal/adminauth -run 'TestVerifyReturnsIssuerBoundIdentity' -v`

Expected: FAIL because `Claims.Identity` is absent.

- [ ] **Step 3: Change only the verifier output**

Replace `Claims.Subject` with `Claims.Identity`, built only after go-oidc has verified signature, issuer, audience, expiry, `nbf`, and `azp`:

```go
id, err := identity.NewHuman(v.cfg.Issuer, idt.Subject)
if err != nil {
	return Claims{}, fmt.Errorf("oidc verify identity: %w", err)
}
return Claims{Identity: id, Groups: groups}, nil
```

Do not add `authz` imports or change `principal.AdminIdentity` in this task. Raw groups remain ephemeral verifier output and still follow the existing mapping path until Task 7 replaces that path.

- [ ] **Step 4: Update claim consumers mechanically**

Replace reads of `claims.Subject` with `claims.Identity.Subject` in the current denial/mint code and update test fixtures to construct `Claims{Identity: identity.ID{...}}`. Preserve current authorization semantics; this task changes identity fidelity only.

- [ ] **Step 5: Run affected tests**

Run: `go test ./internal/adminauth ./internal/server ./internal/controlplane ./cmd/mayu ./cmd/inferplaned -race`

Expected: PASS.

- [ ] **Step 6: Commit issuer propagation**

```bash
git add internal/adminauth internal/server internal/controlplane cmd/mayu cmd/inferplaned
git commit -s -m "feat(auth): bind OIDC subjects to issuer"
```

---

### Task 3: Persist identity references on virtual keys

**Files:**
- Modify: `internal/keystore/keystore.go:17-57`
- Modify: `internal/keystore/sqlite.go:19-171,214-379`
- Modify: `internal/keystore/sqlite_test.go`
- Modify: `internal/keystore/sqlite_teams_test.go`
- Modify: `internal/config/config.go:276-286`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `identity.ID`.
- Produces: `keystore.KeyOptions{Organization string, Identity identity.ID, DisplayName string, ...}` and `keystore.Principal.Organization/Identity`.

- [ ] **Step 1: Write migration and round-trip tests**

```go
func TestIdentityRoundTrip(t *testing.T) {
	store := openTestStore(t, "acme")
	id, _ := identity.NewHuman("https://idp.example.com/pool", "alice-sub")
	plain, _, err := store.CreateWithOptions(context.Background(), "alpha", []string{"*"}, KeyOptions{
		Organization: "acme", Identity: id, DisplayName: "Alice",
	})
	if err != nil { t.Fatal(err) }
	got, err := store.Resolve(context.Background(), plain)
	if err != nil { t.Fatal(err) }
	if got.Organization != "acme" || !got.Identity.Equal(id) { t.Fatalf("got=%+v", got) }
}
```

Add a legacy migration test that creates the old `keys` schema, inserts an owner row and an unowned row, opens the new store, and asserts:

- owner row → service identity `inferplane://local/service-account` / `legacy-owner:<owner>`;
- unowned row → service identity `inferplane://local/service-account` / `legacy-key:<key_id>`;
- neither row becomes a human identity.

- [ ] **Step 2: Run migration tests and verify red**

Run: `go test ./internal/keystore -run 'Test(IdentityRoundTrip|MigrationLegacyIdentity)' -v`

Expected: FAIL because identity columns/options do not exist.

- [ ] **Step 3: Extend the schema and migration under the existing exclusive transaction**

Add non-null columns:

```sql
organization      TEXT NOT NULL DEFAULT '',
identity_kind     TEXT NOT NULL DEFAULT '',
identity_issuer   TEXT NOT NULL DEFAULT '',
identity_subject  TEXT NOT NULL DEFAULT '',
display_name      TEXT NOT NULL DEFAULT ''
```

After adding them, migrate empty identities in the same exclusive transaction to typed legacy service identities. Do not copy legacy `owner` into a human identity. Leave `owner` in the physical schema only so old databases remain readable; stop selecting or writing it in new code.

- [ ] **Step 4: Make all new key writes require validated identity**

`CreateWithOptions` and `EnsureKey` must reject an empty organization or invalid identity. `Create` becomes an explicit service-account helper using subject `key:<generated key id>` after key generation; it must never manufacture a human identity.

Change `keyColumns`, `scanPrincipal`, insert/upsert SQL, and `VirtualKeyConfig` to use:

```go
type VirtualKeyConfig struct {
	Organization string `json:"organization"`
	ServiceAccount string `json:"service_account"`
	DisplayName string `json:"display_name,omitempty"`
	// existing key/model/limit fields
}
```

- [ ] **Step 5: Run keystore/config tests**

Run: `go test ./internal/keystore ./internal/config -race`

Expected: PASS.

- [ ] **Step 6: Commit key identity persistence**

```bash
git add internal/keystore internal/config
git commit -s -m "feat(keystore): persist credential identity references"
```

---

### Task 4: Replace free-form policy users with typed identity selectors

**Files:**
- Modify: `api/v1alpha1/types.go:56-74`
- Modify: `internal/policy/policy.go:82-97,192-210`
- Modify: `internal/policy/store.go:273-307`
- Modify: `internal/policy/*_test.go`
- Modify: `deploy/crd/inferplane.dev_governancepolicies.yaml`
- Modify: policy fixtures in `examples/`, `cmd/mayu/*_test.go`, and `internal/controlplane/*_test.go`

**Interfaces:**
- Consumes: `identity.ID`.
- Produces: `v1alpha1.IdentitySelector`, `policy.Subject.Identity *identity.ID`, and `Store.ModelAllowed(team string, id identity.ID, model string, canon func(string) string) bool`.

- [ ] **Step 1: Write failing selector tests**

```go
func TestIdentitySubjectRequiresFullTypedSelector(t *testing.T) {
	doc := validPolicy()
	doc.Spec.Subject = v1alpha1.Subject{Identity: &v1alpha1.IdentitySelector{
		Kind: "human", Issuer: "https://idp.example.com/pool", Subject: "alice-sub",
	}}
	p, err := FromV1Alpha1(doc)
	if err != nil { t.Fatal(err) }
	if p.Subject.Identity == nil || p.Subject.Identity.Subject != "alice-sub" { t.Fatalf("%+v", p.Subject) }
}

func TestLegacyUserSelectorRejectedEvenWithTeam(t *testing.T) {
	_, err := ParseWireDocs([]byte(`apiVersion: inferplane.dev/v1alpha1
kind: GovernancePolicy
metadata: {name: legacy}
spec: {subject: {team: alpha, user: alice}, rules: []}`))
	if err == nil || !strings.Contains(err.Error(), "subject.user is unsupported") { t.Fatalf("err=%v", err) }
}
```

Also test same `sub` at another issuer does not match, and service selectors match only service identities.

- [ ] **Step 2: Run policy tests and verify red**

Run: `go test ./internal/policy -run 'Test(IdentitySubject|LegacyUser|ModelAllowed)' -v`

Expected: FAIL because typed selectors and explicit legacy rejection are absent.

- [ ] **Step 3: Add the wire selector and strict legacy rejection**

```go
type IdentitySelector struct {
	Kind    string `json:"kind"`
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

type Subject struct {
	Team     string            `json:"team,omitempty"`
	Identity *IdentitySelector `json:"identity,omitempty"`
	LegacyUser string          `json:"user,omitempty"` // decode-only; conversion always rejects
}
```

The `LegacyUser` field is a fail-closed migration detector, not an authority path. `FromV1Alpha1` rejects it before any team-only conversion. Convert validated selectors to `identity.ID` and update `Subject.matches`/`ModelAllowed` to exact typed equality.

- [ ] **Step 4: Update CRD and fixtures**

Use this shape everywhere:

```yaml
subject:
  team: alpha
  identity:
    kind: human
    issuer: https://idp.example.com/pool
    subject: alice-sub
```

Keep team-only documents unchanged. Update CRD required fields and enums for `kind`.

- [ ] **Step 5: Run schema, policy, and command tests**

Run: `go test ./api/v1alpha1 ./internal/policy ./cmd/mayu ./internal/controlplane -race`

Expected: PASS.

- [ ] **Step 6: Commit typed policy subjects**

```bash
git add api/v1alpha1 internal/policy deploy/crd examples cmd/mayu internal/controlplane
git commit -s -m "feat(policy): match durable typed identities"
```

---

### Task 5: Implement fixed roles, capabilities, scopes, and binding stores

**Files:**
- Create: `internal/authz/model.go`
- Create: `internal/authz/authorize.go`
- Create: `internal/authz/store.go`
- Create: `internal/authz/sqlite.go`
- Create: `internal/authz/postgres.go`
- Create: `internal/authz/model_test.go`
- Create: `internal/authz/authorize_test.go`
- Create: `internal/authz/sqlite_test.go`
- Create: `internal/authz/postgres_test.go`

**Interfaces:**
- Consumes: `identity.ID`.
- Produces:

```go
type Role string
type Capability string
type Scope struct { Organization string; Teams []string }
type Grant struct { Role Role; Scope Scope }
type Selector struct { Group string; Identity *identity.ID }
type Binding struct { ID string; Selector Selector; Grant Grant }
type Store interface { List(context.Context) ([]Binding, error); Get(context.Context, string) (Binding, bool, error); Put(context.Context, Binding) error; Delete(context.Context, string) error }
type Resolver struct { /* bootstrap + store */ }
func (r *Resolver) Resolve(context.Context, identity.ID, []string) ([]Grant, error)
func Allows([]Grant, Capability, organization, team string) bool
```

- [ ] **Step 1: Write a closed capability-table test**

Define capabilities for self identity, keys, teams, policy, providers, pricing, usage, audit metadata, audit bodies, fleet, role bindings, and config export. Test representative invariants:

```go
func TestFixedRoleCapabilities(t *testing.T) {
	assertAllows(t, PlatformAdmin, RoleBindingsWrite)
	assertAllows(t, PolicyAdmin, PolicyWrite)
	assertDenies(t, PolicyAdmin, ProviderWrite)
	assertAllows(t, ProviderAdmin, ProviderProbe)
	assertAllows(t, BudgetAdmin, BudgetWrite)
	assertAllows(t, Auditor, AuditRead)
	assertDenies(t, Auditor, AuditBodyRead)
	assertAllows(t, TeamAdmin, KeyIssue)
	assertDenies(t, TeamAdmin, BudgetWrite)
}
```

- [ ] **Step 2: Write scope and store tests**

Cover organization mismatch, team intersection, duplicate binding IDs, invalid role, both/neither selector forms, SQLite reopen, Postgres migration advisory lock, and store failure. A resolver store failure must deny non-bootstrap callers; break-glass/bootstrap platform grants remain usable.

- [ ] **Step 3: Run authz tests and verify red**

Run: `go test ./internal/authz -race -v`

Expected: FAIL because the package does not exist.

- [ ] **Step 4: Implement the fixed table and pure authorization**

Keep `roleCapabilities` private and exhaustive. `platform-admin` includes every declared capability; add a test that iterates all constants. `Scope.Validate` rejects empty organization, duplicate/empty team names, and organization-wide bindings that also carry teams.

- [ ] **Step 5: Implement SQLite and Postgres stores**

Use portable columns (`binding_id`, selector kind/value, role, organization, teams JSON, timestamps). SQLite uses the existing exclusive migration pattern; Postgres uses a package-specific advisory lock and bounded contexts. Store identity selectors as kind/issuer/subject columns—never as an ambiguous concatenated string.

- [ ] **Step 6: Run the authorization package tests**

Run: `go test ./internal/authz -race`

Expected: PASS. This task must not alter `principal.AdminIdentity`; the authorization core is independently reviewable before HTTP integration.

- [ ] **Step 7: Commit authorization core**

```bash
git add internal/authz
git commit -s -m "feat(authz): add fixed scoped management roles"
```

---

### Task 6: Add authorization bootstrap config and identity-aware credential minting

**Files:**
- Modify: `internal/config/config.go:100-158,343-369,1074-1141`
- Modify: `internal/config/config_test.go`
- Modify: `internal/adminauth/mapping.go` and tests
- Modify: `internal/server/adminauth.go` and `internal/server/adminauth_test.go`
- Modify: `internal/server/authapi/authapi.go` and `internal/server/authapi/authapi_test.go`
- Modify: `internal/server/adminapi/keys.go` and `internal/server/adminapi/keys_test.go`
- Modify: `internal/principal/principal.go` and `internal/principal/principal_test.go`
- Modify: `cmd/mayu/gateway.go:1450-1465` and assembly tests

**Interfaces:**
- Consumes: `authz.Resolver`, typed OIDC claims, key options.
- Produces: config `authorization.organization`, bootstrap `bindings`, and service-account key request shape.

- [ ] **Step 1: Write config validation tests**

Use this accepted configuration:

```json
{
  "authorization": {
    "organization": "acme",
    "bindings": [
      {"id":"platform","group":"platform-admins","role":"platform-admin","scope":{"organization":"acme"}},
      {"id":"team-alpha","group":"alpha-leads","role":"team-admin","scope":{"organization":"acme","teams":["alpha"]}}
    ],
    "store":{"type":"sqlite","path":"/var/lib/inferplane/authz.db"}
  }
}
```

Reject legacy `oidc.admin_groups` and `oidc.group_mappings` with a specific migration error, invalid roles/scopes, and OIDC enabled without an organization.

- [ ] **Step 2: Write key mint tests**

Test that CLI OIDC mint persists exactly `claims.Identity`; re-minting produces a new key ID with the same typed identity. Test admin service-key creation accepts only:

```json
{"team":"alpha","allowed_models":["*"],"service_account":"build-bot","display_name":"Build bot"}
```

and derives the service issuer server-side. A non-platform caller cannot mint a human identity on another user's behalf.

- [ ] **Step 3: Run tests and verify red**

Run: `go test ./internal/config ./internal/server/adminauth ./internal/server/authapi ./internal/server/adminapi -race`

Expected: FAIL on the new config and identity assertions.

- [ ] **Step 4: Replace mapping with resolver assembly**

`AdminAuth` verifies the bearer, calls `Resolver.Resolve(ctx, claims.Identity, claims.Groups)`, rejects an empty grant set with 403, and injects `principal.AdminIdentity`. Static break-glass injects a server-created service identity and an organization-wide `platform-admin` grant. Raw groups are discarded immediately after resolution.

- [ ] **Step 5: Update key APIs**

Remove `owner` from accepted JSON and authorization decisions. Keep `display_name` informational. CLI mint copies the verified human ID; admin mint creates a typed service ID. Use the full identity value—not only subject—for mint throttling.

- [ ] **Step 6: Run focused and aggregate tests**

Run: `go test ./internal/config ./internal/adminauth ./internal/server/... ./cmd/mayu -race`

Expected: PASS.

- [ ] **Step 7: Commit bootstrap and minting**

```bash
git add internal/config internal/adminauth internal/server cmd/mayu
git commit -s -m "feat(authz): resolve scoped grants at authentication"
```

---

### Task 7: Gate and scope every `mayu` management endpoint

**Files:**
- Create: `internal/server/authz.go`
- Create: `internal/server/adminapi/rolebindings.go`
- Create: `internal/server/adminapi/rolebindings_test.go`
- Modify: `internal/server/server.go:173-337`
- Modify: `internal/server/server_test.go:560-806`
- Modify: `internal/server/adminapi/{keys,teams,whoami,bodies}.go` and tests
- Modify: `internal/server/analyticsapi/*.go` and tests
- Modify: `internal/server/configapi/*.go` and tests
- Modify: `internal/server/auditapi/auditapi.go` and tests

**Interfaces:**
- Consumes: `AdminIdentity.Allows`, `authz.Store`.
- Produces: `Require(capability, teamResolver, emit, next)` and `/admin/role-bindings` CRUD.

- [ ] **Step 1: Add a table-driven route matrix test before changing mounts**

The test authenticates one identity per fixed role and checks this matrix:

| Endpoint | Capability | Scope behavior |
|---|---|---|
| `GET /admin/whoami` | authenticated self | own identity only |
| `GET/POST/DELETE /admin/keys` | `key.read/issue/revoke` | rows/target key filtered by teams |
| `GET /admin/teams` | `team.read` | rows filtered by teams |
| `PUT/DELETE /admin/teams/{team}` | field-dependent `policy.write` and/or `budget.write` | exact team; team-admin denied |
| provider/model/config/catalog/health | `provider.read/write/probe` | organization |
| analytics/logs/alerts | `usage.read` or `budget.read` | query forcibly intersected with teams |
| audit verify | `audit.read` | organization |
| body fetch/delete | `audit.body.read/delete` | platform-admin only |
| role bindings | `role-bindings.read/write` | platform-admin only |

A list endpoint returning cross-team rows is a test failure even when status is 200.

- [ ] **Step 2: Run matrix tests and verify red**

Run: `go test ./internal/server/... -run 'TestAdminRouteMatrix|TestScoped' -v`

Expected: FAIL because routes still share coarse `AdminAuth`/`requireAdmin` gates.

- [ ] **Step 3: Implement one authorization middleware**

```go
type TeamResolver func(*http.Request) (string, error)
func Require(cap authz.Capability, resolve TeamResolver, emit func(audit.Record), next http.Handler) http.Handler
```

Missing identity → 401; missing capability/scope → 403 plus `admin_denied` containing requested capability and scope. Resolver/read errors fail closed with 500, never by skipping scope.

- [ ] **Step 4: Make list/query handlers scope-aware**

Pass an immutable `authz.ScopeFilter` derived from context. Filter key/team/user lists before encoding. Force analytics/usage queries to the allowed team intersection; do not trust a caller-supplied `team` query. Organization-scoped roles retain full views.

- [ ] **Step 5: Split team-record field authorization**

Load the existing team record, diff fields, and require:

- `policy.write` for allowed models, regions, and guardrails;
- `budget.write` for RPM, TPM, token quota, budget, and exceeded behavior.

A request changing both groups needs both capabilities. A delete needs both because it removes both policy and budget state. This avoids creating overlapping endpoints while preserving separation of duties.

- [ ] **Step 6: Add role-binding CRUD**

Only organization-scoped platform admins can list/write/delete bindings. Validate before storage, forbid deleting the last non-break-glass platform-admin bootstrap path, and never expose raw groups beyond selector names already configured as authorization data.

- [ ] **Step 7: Update `whoami` and UI bootstrap DTO**

Return issuer digest, subject, identity kind, organization, roles, scopes, and effective capabilities. Do not return raw issuer, groups, or token claims.

- [ ] **Step 8: Run server tests**

Run: `go test ./internal/server/... -race`

Expected: PASS.

- [ ] **Step 9: Commit the mayu management matrix**

```bash
git add internal/server
git commit -s -m "feat(admin): enforce scoped capabilities on mayu"
```

---

### Task 8: Separate `inferplaned` machine and management channels

**Files:**
- Create: `internal/controlplane/managementauth.go`
- Create: `internal/controlplane/rolebindings.go`
- Create: `internal/controlplane/rolebindings_test.go`
- Modify: `internal/controlplane/auth.go` and tests
- Modify: `internal/controlplane/controlplane.go:304-310`
- Modify: `internal/controlplane/usage.go:40-45`
- Modify: `internal/controlplane/policies.go:170-174`
- Modify: `internal/controlplane/export.go:21-23`
- Modify: `cmd/inferplaned/oidcenv.go` and tests
- Modify: `cmd/inferplaned/main.go:140-253` and tests

**Interfaces:**
- Consumes: `authz.Resolver/Store`, typed OIDC claims.
- Produces: separate `machineAuthn` and `managementAuthn + Require` paths.

- [ ] **Step 1: Write channel-confusion tests**

Assert:

- management OIDC token receives 403 on sync, usage ingest, and credentials;
- data-plane token receives 401/403 on policy writes, usage query/export, role bindings, and dataplane list;
- broker token remains accepted only by credentials;
- management break-glass gets organization-scoped platform admin;
- empty auth is allowed only for explicit loopback development mode and never when mutable stores are enabled.

- [ ] **Step 2: Run control-plane tests and verify red**

Run: `go test ./internal/controlplane ./cmd/inferplaned -run 'Test(Channel|Machine|Management|RoleBinding)' -v`

Expected: FAIL because `authn` currently grants one token/OIDC identity whole-console access.

- [ ] **Step 3: Introduce explicit environment inputs**

Replace ambiguous `INFERPLANED_TOKEN` use with:

```text
INFERPLANED_DATAPLANE_TOKEN       machine sync + usage ingest
INFERPLANED_MANAGEMENT_TOKEN      opaque break-glass management credential
INFERPLANED_ORGANIZATION          required for non-loopback management
INFERPLANED_AUTHZ_BINDINGS_FILE   bootstrap binding JSON/YAML
INFERPLANED_AUTHZ_DSN             optional mutable Postgres binding store
```

Keep `INFERPLANED_BROKER_TOKEN` separate. Reject equal token values and JWT-shaped static tokens. This is an alpha breaking change; fail boot with migration instructions rather than aliasing the old variable.

- [ ] **Step 4: Mount routes through the explicit matrix**

Machine routes:

- `POST /v1alpha1/sync`
- `POST /v1alpha1/usage`
- `POST /v1alpha1/credentials` (broker token only)

Management routes:

- `GET /v1alpha1/dataplanes` → `fleet.read`
- `GET /v1alpha1/config/export` → `policy.read`
- `GET/PUT/DELETE /v1alpha1/policies` → `policy.read/write`
- `GET /v1alpha1/usage`, export → `usage.read` with team scope
- role-binding CRUD → platform admin capabilities

- [ ] **Step 5: Add mutable central bindings**

When `INFERPLANED_AUTHZ_DSN` is set, bootstrap bindings seed once under a durable marker and Postgres becomes authoritative. Without it, bindings are file-authoritative and writes return 405. Never infer “unseeded” from an empty row count.

- [ ] **Step 6: Run control-plane and command tests**

Run: `go test ./internal/controlplane ./cmd/inferplaned -race`

Expected: PASS.

- [ ] **Step 7: Commit channel separation**

```bash
git add internal/controlplane cmd/inferplaned
git commit -s -m "feat(controlplane): separate machine and management auth"
```

---

### Task 9: Add durable identity and mutation evidence to the audit chain

**Files:**
- Create: `internal/audit/mutation.go`
- Create: `internal/audit/mutation_test.go`
- Modify: `internal/audit/record.go:10-126`
- Modify: `internal/audit/record_test.go`
- Modify: `internal/server/adminapi/{keys,teams,rolebindings}.go`
- Modify: `internal/server/configapi/write.go`
- Modify: `internal/controlplane/{policies,rolebindings}.go`
- Modify: `cmd/mayu/gateway.go` reload path
- Modify: `cmd/inferplaned/main.go`

**Interfaces:**
- Consumes: typed actor identity, authorization capability/scope, canonical resource bytes.
- Produces: appended `PrincipalRef` identity fields and `MutationRef`.

- [ ] **Step 1: Write canonical digest and compatibility tests**

```go
func TestDigestIsDeterministicAndDomainSeparated(t *testing.T) {
	a := Digest("policy", []byte("body"))
	b := Digest("provider", []byte("body"))
	if a == b || !strings.HasPrefix(a, "sha256:") { t.Fatalf("a=%q b=%q", a, b) }
}

func TestMutationFieldsAreOmittedOnLegacyRecord(t *testing.T) {
	b, _ := Record{SchemaVersion: 1, Event: "request_started"}.Canonical()
	if bytes.Contains(b, []byte("mutation")) || bytes.Contains(b, []byte("issuer_id")) {
		t.Fatalf("legacy bytes changed: %s", b)
	}
}
```

- [ ] **Step 2: Run audit tests and verify red**

Run: `go test ./internal/audit -run 'Test(Digest|Mutation|Identity)' -v`

Expected: FAIL because mutation/identity evidence is absent.

- [ ] **Step 3: Append audit fields**

Append to `PrincipalRef`:

```go
IdentityKind *string `json:"identity_kind,omitempty"`
IssuerID     *string `json:"issuer_id,omitempty"`
Subject      *string `json:"subject,omitempty"`
```

Append to `Record`:

```go
Mutation *MutationRef `json:"mutation,omitempty"`
```

`MutationRef` contains capability, organization, optional team, resource kind/name, before/after SHA-256, and resulting generation. Never include resource body.

- [ ] **Step 4: Centralize mutation creation**

```go
func NewMutationRecord(event string, actor principal.AdminIdentity, cap authz.Capability, scope authz.Scope, resourceKind, resourceName string, before, after []byte, generation string) audit.Record
```

Place the constructor outside `audit` if importing `principal` would create a cycle; `audit` owns only `Digest` and DTOs, while `server`/`controlplane` adapters build records.

- [ ] **Step 5: Instrument all Phase 0 mutation paths**

- policy put/delete: hash stored bytes before and after, include policy generation;
- provider/model put/delete: return mutation metadata from assembly writer after successful build-persist-swap, include live generation;
- team put/delete: canonical JSON of read-back records; changed budget/policy capabilities in evidence;
- role-binding put/delete: canonical JSON from store;
- pricing SIGHUP reload: emit only when pricing table version or canonical rate hash changes.

Emit success only after durable mutation succeeds. Emit denial separately before mutation. On required audit back-pressure, retain the existing writer’s blocking semantics; do not make mutation audit best-effort.

- [ ] **Step 6: Configure an `inferplaned` management audit writer**

Require `INFERPLANED_AUDIT_FILE` whenever policy or role-binding writes are enabled; use `INFERPLANED_AUDIT_WAL` or derive `<file>.wal`. Refuse boot if mutable management is enabled without durable audit. Read-only/file-authoritative operation remains available without it.

- [ ] **Step 7: Add mutation integration tests**

For each mutation, decode emitted JSONL and assert actor issuer digest/subject, capability, scope, non-empty before/after hashes, and resulting generation. Assert no submitted YAML, group token, provider ref value, or team body appears in the record.

- [ ] **Step 8: Run audit and mutation-owner packages**

Run: `go test ./internal/audit ./internal/server/... ./internal/controlplane ./cmd/mayu ./cmd/inferplaned -race`

Expected: PASS.

- [ ] **Step 9: Commit audit evidence**

```bash
git add internal/audit internal/server internal/controlplane cmd/mayu cmd/inferplaned
git commit -s -m "feat(audit): record scoped management mutations"
```

---

### Task 10: Propagate durable identity through policy, usage, and inference audit

**Files:**
- Modify: `cmd/mayu/gateway.go:440-446`
- Modify: `internal/server/{anthropicapi/messages.go,openaiapi/chat.go,bedrockapi/invoke.go}`
- Modify: `internal/telemetry/usage.go`
- Modify: `internal/telemetry/collector.go`
- Modify: matching tests and `cmd/mayu/identity_e2e_test.go`

**Interfaces:**
- Consumes: `keystore.Principal.Identity`, typed `policy.Store.ModelAllowed`, appended audit identity fields.
- Produces: one stable identity reference across inference audit and usage windows.

- [ ] **Step 1: Write the key-rotation/two-device integration test**

Mint two CLI keys from the same OIDC `(issuer, sub)` for different simulated data planes, issue one request with each, and assert:

- different `key_id` values;
- identical typed user identity in policy decisions;
- usage rows aggregate under the same issuer digest + subject identity key;
- request_started/completed records carry the same durable identity and their own key IDs.

- [ ] **Step 2: Run the integration test and verify red**

Run: `go test ./cmd/mayu -run TestOIDCIdentitySurvivesKeyRotationAndDevices -v`

Expected: FAIL because model policy, telemetry, and request audit still use owner/key-only identity.

- [ ] **Step 3: Replace owner-based policy and telemetry calls**

```go
return polStore.ModelAllowed(p.Team, p.Identity, model, canonical)
```

Change usage entry identity from `User string` to a structured reference (`kind`, `issuer_id`, `subject`) or a dedicated comparable key encoded into separate DB columns. Do not concatenate unescaped fields into one string.

- [ ] **Step 4: Add identity to every ingress audit site**

Use one helper to build `audit.PrincipalRef` for Anthropic, OpenAI, and Bedrock handlers. This prevents one ingress from silently omitting issuer/subject. Preserve key ID and team for credential-level investigation.

- [ ] **Step 5: Run all ingress, telemetry, and command tests**

Run: `go test ./internal/server/anthropicapi ./internal/server/openaiapi ./internal/server/bedrockapi ./internal/telemetry ./cmd/mayu -race`

Expected: PASS.

- [ ] **Step 6: Commit data-path attribution**

```bash
git add cmd/mayu internal/server internal/telemetry
git commit -s -m "feat(identity): attribute policy usage and audit by user"
```

---

### Task 11: Make consoles capability-aware without weakening server checks

**Files:**
- Modify: `internal/server/adminapi/whoami.go` and tests
- Modify: `internal/server/adminui/static/app.js`
- Modify: `internal/server/adminui/static/i18n.js`
- Modify: `internal/controlplane/ui/static/app.js`
- Modify: UI package tests

**Interfaces:**
- Consumes: effective roles/capabilities/scopes returned by whoami or management bootstrap endpoint.
- Produces: hidden/disabled actions with explicit “insufficient capability” state.

- [ ] **Step 1: Add DTO allowlist tests**

Assert the JSON includes only identity kind, issuer digest, subject, organization, role/scope summaries, and capability strings. Assert it excludes raw issuer, groups, email, token, and store internals.

- [ ] **Step 2: Run UI/backend tests and verify red**

Run: `go test ./internal/server/adminapi ./internal/server/adminui ./internal/controlplane/ui -v`

Expected: FAIL because current DTO exposes legacy fields and UI assumes coarse admin state.

- [ ] **Step 3: Update UI affordances**

Read effective capabilities once after login. Hide mutation controls when absent, filter team selectors to scoped teams, and render a read-only banner for auditors/team admins. A 403 response still displays as authoritative even if the UI incorrectly showed a control.

- [ ] **Step 4: Run static UI tests**

Run: `go test ./internal/server/adminui ./internal/controlplane/ui ./internal/server/adminapi`

Expected: PASS.

- [ ] **Step 5: Exercise the UI in a browser**

Run a local `mayu` with test OIDC fixtures or the existing test harness, then verify platform-admin, auditor, provider-admin, and team-admin golden paths plus a forbidden direct API call. Capture console/network errors; do not report UI completion if this manual/browser check cannot be performed.

- [ ] **Step 6: Commit capability-aware consoles**

```bash
git add internal/server/adminapi internal/server/adminui internal/controlplane/ui
git commit -s -m "feat(ui): render scoped management capabilities"
```

---

### Task 12: Prove the Phase 0 exit gate and synchronize documentation

**Files:**
- Create: `cmd/inferplaned/authz_e2e_test.go`
- Modify: `cmd/mayu/identity_e2e_test.go`
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/onboarding.md`
- Modify: `docs/api-reference.md`
- Modify: `docs/reference/api.md`
- Modify: `docs/reference/data.md`
- Modify: `docs/reference/security.md`
- Modify: `docs/reference/infrastructure.md`
- Modify: `docs/roadmap.md`
- Modify: `docs/enterprise-strategy.md`
- Modify: `CLAUDE.md`, `cmd/CLAUDE.md`, `internal/CLAUDE.md`

**Interfaces:**
- Consumes: all Phase 0 contracts.
- Produces: executable exit-gate evidence and current documentation.

- [ ] **Step 1: Add the cross-role negative matrix E2E test**

Start real `httptest` mayu/inferplaned handlers with two users in one team, one user on two credentials, all fixed roles, a data-plane token, management token, and broker token. Verify every allowed and denied matrix cell, scoped list filtering, and audit evidence for denials/mutations.

- [ ] **Step 2: Run exit-gate tests**

Run:

```bash
go test ./cmd/mayu -run 'TestOIDCIdentitySurvivesKeyRotationAndDevices|TestManagementRoleMatrix' -race -v
go test ./cmd/inferplaned -run 'TestMachineManagementChannelSeparation|TestManagementRoleMatrix|TestMutationAudit' -race -v
```

Expected: PASS.

- [ ] **Step 3: Update docs with exact current behavior**

Mark only Phase 0 complete. Document the breaking configuration/API migration, fixed capability matrix, service-account key body, machine/management token split, required central audit configuration, and explicit remaining Phase 1–4 gaps. Do not claim per-user budgets, PII routing, Converse cache-point translation, or fleet-global quotas are complete.

- [ ] **Step 4: Run the full verification gate**

Run:

```bash
go test ./... -race
go vet ./...
test -z "$(gofmt -l .)"
bash tests/run-all.sh
CGO_ENABLED=0 go build -trimpath -o /tmp/inferplane-phase0-mayu ./cmd/mayu
CGO_ENABLED=0 go build -trimpath -o /tmp/inferplane-phase0-inferplaned ./cmd/inferplaned
git diff --check
```

Expected: all commands exit 0; harness reports 67/67 or the new updated total with zero failures.

- [ ] **Step 5: Request security and code review**

Invoke `security-review`, then `inferplane:code-review`, and resolve all confidence ≥75 findings. Re-run Step 4 after any edit.

- [ ] **Step 6: Commit Phase 0 docs and E2E evidence**

```bash
git add cmd/mayu/identity_e2e_test.go cmd/inferplaned/authz_e2e_test.go README.md docs CLAUDE.md cmd/CLAUDE.md internal/CLAUDE.md
git commit -s -m "docs: record Phase 0 identity and management trust"
```

## Follow-on Plan Boundaries

Phase 0 deliberately stops after producing stable identity, scope, capability, and audit contracts. Create separate specs/plans in this order:

1. **Phase 1 — user budget state machine:** enforcement key `(organization, identity, pool, windowID)`, premium/total pools, conservative attempt reservation, centrally pre-reserved fleet authority, approved fallback sets, token-quota behavior, and restart/concurrency proofs.
2. **Phase 2 — pre-egress PII policy:** typed detector/transform plugins, monotonic egress ceiling (`external-unmodified`, `external-masked`, `internal-only`, `blocked`), policy-selected action, PII-free OTel metadata, and correlated audit digest.
3. **Phase 3 — cache and cost efficiency:** Bedrock Converse cache-point mapping, hit/write/waste attribution, model-switch/masking cache loss, live cache reuse tests, and CUR/Application Inference Profile reconciliation.
4. **Phase 4 — fleet operations:** global rate/quota shares, version/update/doctor flows, packaging security defaults, CI/signing, SLOs, and runbooks.

No Phase 1–4 implementation should be pulled into Phase 0 merely because the new identity/capability types make it convenient.
