# P0 Enterprise-Ready Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix 6 P0 critical issues blocking enterprise-ready claim: guardrail bypass, billing bug, per-user governance, duty separation, fleet enforcement, and PII policy.

**Architecture:** 
- Week 1-3: Fix immediate bugs (P0-01, P0-02) in parallel
- Week 4-6: Implement per-user governance (P0-03) with durable identity model
- Week 7-9: Implement duty separation + RBAC (P0-04) and fleet enforcement accuracy (P0-05)
- Week 10-12: Implement PII policy engine (P0-06) and integration testing

**Tech Stack:** Go 1.25, SQLite, Postgres (for HA), Redis (for rate shares), standard library

## Global Constraints

From enterprise-strategy.md:
- **Durable identity:** UserID = (OIDC issuer, subject), stable across key rotation/re-login/second device
- **Duty separation:** Fixed roles with org/team scope, every mutation records actor/capability/hash
- **Two-pool budget:** Premium pool + total hard cap with explicit fallback-or-block
- **Pre-egress PII policy:** Typed detector result, policy engine selects action, egress ceiling attached per-request
- **Fleet enforcement:** Enforcement key ≥ (org, UserID, pool, windowID) in durable ledger
- **Guardrail/residency:** Applied on EVERY egress path, no opt-out reachable from routing config
- **Cost explainability:** Every request settles against immutable pricing version
- **Integer microUSD:** Never float, round-half-even via math/big
- **Two-phase governance:** PreCheck BEFORE billing, Settle AFTER
- **Fail-closed:** Detector/masker/guardrail failures deny before egress

---

## Task 1: Guardrail Bypass Fix (P0-01)

**Files:**
- Create: `providers/bedrock/guardrail_check.go`
- Modify: `providers/bedrock/mantle.go:45-62`
- Modify: `internal/server/bedrockapi/invoke.go:350-360`
- Test: `providers/bedrock/mantle_test.go`

**Interfaces:**
- Consumes: `guardrailFor(team)` from `providers/bedrock/converse.go:230,267`
- Produces: `ErrGuardrailNotSupportedOnMantle` error type, `GuardrailApplied: bool` in audit record

**Priority:** CRITICAL (Week 1, Day 1-2)

- [ ] **Step 1: Write the failing test**

```go
// providers/bedrock/mantle_test.go
func TestMantleRejectsWhenGuardrailConfigured(t *testing.T) {
    cfg := &config.Config{
        Providers: []config.Provider{{
            Kind: "bedrock",
            Bedrock: config.BedrockProvider{
                GuardrailID: "gr-12345",
            },
            ModelAPI: map[string]string{
                "claude-3-opus": "mantle",
            },
        }},
    }
    
    p, err := bedrock.NewProvider(cfg.Providers[0])
    require.NoError(t, err)
    
    req := &bedrock.Request{
        Model: "claude-3-opus",
        Body:  []byte(`{"messages":[{"role":"user","content":"test"}]}`),
    }
    
    _, err = p.Complete(context.Background(), req)
    assert.ErrorIs(t, err, bedrock.ErrGuardrailNotSupportedOnMantle)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd providers/bedrock && go test -run TestMantleRejectsWhenGuardrailConfigured -v`
Expected: FAIL with "guardrail check not implemented"

- [ ] **Step 3: Define error type**

```go
// providers/bedrock/errors.go
var (
    // ErrGuardrailNotSupportedOnMantle indicates Mantle path cannot apply guardrails
    ErrGuardrailNotSupportedOnMantle = errors.New(
        "guardrail configured but Mantle API does not support Bedrock Guardrails",
    )
)
```

- [ ] **Step 4: Add guardrail check to Mantle path**

```go
// providers/bedrock/mantle.go
func (p *Provider) Complete(ctx context.Context, req *Request) (*Response, error) {
    // Guardrail check: Mantle doesn't support Bedrock Guardrails
    if p.guardrailID != "" {
        return nil, ErrGuardrailNotSupportedOnMantle
    }
    
    // ... existing implementation
}

func (p *Provider) Stream(ctx context.Context, req *Request) (*StreamResponse, error) {
    // Guardrail check: Mantle doesn't support Bedrock Guardrails
    if p.guardrailID != "" {
        return nil, ErrGuardrailNotSupportedOnMantle
    }
    
    // ... existing implementation
}
```

- [ ] **Step 5: Add audit record field**

```go
// internal/audit/record.go
type Record struct {
    // ... existing fields
    
    // GuardrailApplied indicates whether guardrail was actually applied (false on Mantle bypass)
    GuardrailApplied bool `json:"guardrail_applied"`
}
```

- [ ] **Step 6: Update audit write**

```go
// internal/server/bedrockapi/invoke.go:356
auditRecord := audit.Record{
    // ... existing fields
    
    // Guardrail applied only on Converse/InvokeModel paths, not Mantle
    GuardrailApplied: resp.GuardrailApplied,
}
```

- [ ] **Step 7: Run all tests**

Run: `go test ./providers/bedrock/... -v`
Expected: PASS (including new test)

- [ ] **Step 8: Commit**

```bash
git add providers/bedrock/errors.go providers/bedrock/mantle.go internal/audit/record.go internal/server/bedrockapi/invoke.go providers/bedrock/mantle_test.go
git commit -m "fix(bedrock): fail-closed guardrail check on Mantle path (P0-01)

- Add ErrGuardrailNotSupportedOnMantle error
- Guardrail configured → Mantle request rejected
- Add GuardrailApplied field to audit record

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Zero-Cost Billing Fix (P0-02)

**Files:**
- Create: `providers/bedrock/usage_extract.go`
- Modify: `providers/bedrock/mantle.go:210-230`
- Modify: `internal/server/bedrockapi/invoke.go:330-340`
- Test: `providers/bedrock/usage_extract_test.go`

**Interfaces:**
- Consumes: PreCheck estimated usage from governor
- Produces: Always-populated `Usage` field in `Response`, even when `Parsed` is nil

**Priority:** CRITICAL (Week 1, Day 3-5)

- [ ] **Step 1: Write the failing test**

```go
// providers/bedrock/usage_extract_test.go
func TestExtractUsageFromMalformedResponse(t *testing.T) {
    // Malformed response body with valid usage field
    body := []byte(`{"usage":{"input_tokens":100,"output_tokens":50},"choices":"malformed"}`)
    
    usage, err := bedrock.ExtractUsage(body)
    require.NoError(t, err)
    
    assert.Equal(t, 100, usage.InputTokens)
    assert.Equal(t, 50, usage.OutputTokens)
}

func TestExtractUsageFromEmptyBody(t *testing.T) {
    body := []byte(`{}`)
    
    usage, err := bedrock.ExtractUsage(body)
    assert.Error(t, err) // Expected to fail, caller uses estimate
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd providers/bedrock && go test -run TestExtractUsage -v`
Expected: FAIL with "function not defined"

- [ ] **Step 3: Implement usage extraction**

```go
// providers/bedrock/usage_extract.go
package bedrock

import (
    "encoding/json"
    
    "github.com/inferplane/inferplane/pkg/schema"
)

// ExtractUsage parses just the usage fields from a response body, even when the
// rest of the body is malformed. Returns error only if usage is completely missing.
func ExtractUsage(body []byte) (schema.Usage, error) {
    var raw struct {
        Usage struct {
            InputTokens  int `json:"input_tokens"`
            OutputTokens int `json:"output_tokens"`
        } `json:"usage"`
    }
    
    if err := json.Unmarshal(body, &raw); err != nil {
        return schema.Usage{}, err
    }
    
    // At minimum, we need input_tokens
    if raw.Usage.InputTokens == 0 {
        return schema.Usage{}, errors.New("usage.input_tokens is zero")
    }
    
    return schema.Usage{
        InputTokens:  raw.Usage.InputTokens,
        OutputTokens: raw.Usage.OutputTokens,
    }, nil
}
```

- [ ] **Step 4: Add fallback in Mantle response**

```go
// providers/bedrock/mantle.go:210-230
func (p *Provider) Complete(...) (*Response, error) {
    // ... existing code ...
    
    // Always extract usage, even if body is malformed
    usage, err := ExtractUsage(rawBody)
    if err != nil {
        // Log warning but continue - caller will use PreCheck estimate
        p.logger.Warn("usage extraction failed, using estimate", "error", err)
        usage = estimatedUsage // From PreCheck
    }
    
    return &Response{
        Parsed:     parsed,     // May be nil on parse failure
        Usage:      usage,     // ALWAYS populated
        RawBody:    rawBody,
        GuardrailApplied: false, // Mantle doesn't support guardrails
    }, nil
}
```

- [ ] **Step 5: Update settlement logic**

```go
// internal/server/bedrockapi/invoke.go:334
// BEFORE:
if resp.Parsed != nil {
    governor.Settle(...)
}

// AFTER:
// Always settle - use Usage field (populated from wire or estimate)
governor.Settle(ctx, principal, resp.Usage, cost, model, provider)
```

- [ ] **Step 6: Add Prometheus metric**

```go
// internal/metrics/metrics.go
var (
    MantleZeroCostTotal = promauto.NewCounter(prometheus.CounterOpts{
        Name: "inferplane_mantle_zero_cost_total",
        Help: "Count of Mantle responses that billed zero (should be zero)",
    })
)

// internal/server/bedrockapi/invoke.go:340
if cost == 0 {
    metrics.MantleZeroCostTotal.Inc()
    p.logger.Error("zero-cost Mantle response", "model", model)
}
```

- [ ] **Step 7: Run all tests**

Run: `go test ./providers/bedrock/... ./internal/server/bedrockapi/... -v`
Expected: PASS (all tests, including zero-cost alert)

- [ ] **Step 8: Commit**

```bash
git add providers/bedrock/usage_extract.go providers/bedrock/mantle.go internal/server/bedrockapi/invoke.go internal/metrics/metrics.go providers/bedrock/usage_extract_test.go
git commit -m "fix(bedrock): always settle Mantle usage, extract separately (P0-02)

- ExtractUsage function parses usage even from malformed responses
- Fallback to PreCheck estimate when extraction fails
- Add inferplane_mantle_zero_cost_total metric

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Durable Identity Schema (P0-03 Phase 1)

**Files:**
- Modify: `internal/keystore/schema.sql`
- Modify: `internal/keystore/store.go`
- Create: `internal/principal/userid.go`
- Test: `internal/keystore/userid_test.go`

**Interfaces:**
- Consumes: OIDC token (issuer, subject) from login flow
- Produces: `UserID` type, stable across key rotations

**Priority:** CRITICAL (Week 4, Day 1-2)

- [ ] **Step 1: Write the failing test**

```go
// internal/keystore/userid_test.go
func TestUserIDStableAcrossKeyRotation(t *testing.T) {
    db := testDB(t)
    store := keystore.NewStore(db)
    
    // Create key with UserID
    key1, err := store.Create(context.Background(), keystore.KeyCreate{
        Team:    "demo",
        Owner:   "user@example.com",
        UserID:  "auth.example.com|user123",
        Issuer:  "auth.example.com",
        Subject: "user123",
    })
    require.NoError(t, err)
    
    // Rotate key
    key2, err := store.Create(context.Background(), keystore.KeyCreate{
        Team:    "demo",
        Owner:   "user@example.com",
        UserID:  "auth.example.com|user123", // Same UserID
        Issuer:  "auth.example.com",
        Subject: "user123",
    })
    require.NoError(t, err)
    
    // Verify same UserID
    assert.Equal(t, key1.UserID, key2.UserID)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/keystore/... -run TestUserIDStableAmongKeyRotation -v`
Expected: FAIL with "column user_id does not exist"

- [ ] **Step 3: Add schema migration**

```sql
-- internal/keystore/schema.sql
ALTER TABLE keys ADD COLUMN user_id TEXT;
ALTER TABLE keys ADD COLUMN issuer TEXT;
ALTER TABLE keys ADD COLUMN subject TEXT;

CREATE INDEX idx_keys_user_id ON keys(user_id);

-- Migration: populate from existing owner field (best effort)
UPDATE keys SET user_id = issuer || '|' || subject WHERE issuer IS NOT NULL AND subject IS NOT NULL;
```

- [ ] **Step 4: Define UserID type**

```go
// internal/principal/userid.go
package principal

import (
    "fmt"
    "strings"
)

// UserID is a stable identity across key rotations, re-logins, and second devices.
// Format: "issuer|subject" (e.g., "https://auth.example.com|user123")
type UserID struct {
    Issuer string
    Sub    string
}

func (u UserID) String() string {
    return fmt.Sprintf("%s|%s", u.Issuer, u.Sub)
}

func ParseUserID(s string) (UserID, error) {
    parts := strings.SplitN(s, "|", 2)
    if len(parts) != 2 {
        return UserID{}, fmt.Errorf("invalid UserID format: %s", s)
    }
    return UserID{Issuer: parts[0], Sub: parts[1]}, nil
}
```

- [ ] **Step 5: Update Key struct**

```go
// internal/keystore/store.go
type Key struct {
    ID        string
    Team      string
    Owner     string // Free-form, unstable
    
    // Durable identity (stable across key rotations)
    UserID    string // Format: "issuer|subject"
    Issuer    string
    Subject   string
    
    CreatedAt time.Time
    ExpiresAt time.Time
    Models    []string
}
```

- [ ] **Step 6: Run migration and tests**

Run: `go test ./internal/keystore/... -v`
Expected: PASS (schema migration succeeds, tests pass)

- [ ] **Step 7: Commit**

```bash
git add internal/keystore/schema.sql internal/keystore/store.go internal/principal/userid.go internal/keystore/userid_test.go
git commit -m "feat(identity): add durable UserID schema (P0-03 phase 1)

- Add user_id, issuer, subject columns to keys table
- Define UserID type with stable format \"issuer|subject\"
- Backward compatible: keeper owner column

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: OIDC Identity Extraction (P0-03 Phase 1)

**Files:**
- Modify: `internal/adminauth/oidc.go`
- Modify: `cmd/mayu/login.go`
- Test: `internal/adminauth/oidc_userid_test.go`

**Interfaces:**
- Consumes: OIDC ID token from provider
- Produces: `UserID` extracted from token claims (iss, sub)

**Priority:** CRITICAL (Week 4, Day 3-4)

- [ ] **Step 1: Write the failing test**

```go
// internal/adminauth/oidc_userid_test.go
func TestExtractUserIDFromOIDCToken(t *testing.T) {
    // Mock OIDC token with iss and sub claims
    token := &oidc.IDToken{
        Issuer:  "https://auth.example.com",
        Subject: "user123",
    }
    
    userID := adminauth.ExtractUserID(token)
    
    assert.Equal(t, "https://auth.example.com|user123", userID.String())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/adminauth/... -run TestExtractUserIDFromOIDCToken -v`
Expected: FAIL with "function not defined"

- [ ] **Step 3: Implement extraction**

```go
// internal/adminauth/oidc.go
func ExtractUserID(token *oidc.IDToken) principal.UserID {
    return principal.UserID{
        Issuer: token.Issuer,
        Sub:    token.Subject,
    }
}
```

- [ ] **Step 4: Wire into login flow**

```go
// cmd/mayu/login.go
func (c *LoginCommand) Run(ctx context.Context) error {
    // ... existing OIDC flow ...
    
    // Extract UserID from token
    userID := adminauth.ExtractUserID(token)
    
    // Create key with durable identity
    key, err := c.store.Create(ctx, keystore.KeyCreate{
        Team:    c.team,
        Owner:   token.Email, // Legacy field, may differ per device
        UserID:  userID.String(),
        Issuer:  userID.Issuer,
        Subject: userID.Sub,
        Expires: token.Expiry,
        Models:  []string{"*"},
    })
    
    // ... rest of flow
}
```

- [ ] **Step 5: Update Principal struct**

```go
// internal/principal/principal.go
type Principal struct {
    KeyID   string
    Team    string
    Owner   string // Free-form, unstable (legacy)
    
    // Durable identity
    UserID  UserID // Stable across key rotations
}
```

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/adminauth/... ./cmd/mayu/... -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/adminauth/oidc.go cmd/mayu/login.go internal/principal/principal.go internal/adminauth/oidc_userid_test.go
git commit -m "feat(login): extract durable UserID from OIDC token (P0-03 phase 1)

- ExtractUserID function parses iss/sub claims
- Wire into mayu login flow
- Update Principal struct with UserID field

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 5: Per-User Governance Keys (P0-03 Phase 2)

**Files:**
- Modify: `internal/policy/store.go:168-169`
- Create: `internal/governance/userkey.go`
- Test: `internal/policy/per_user_test.go`

**Interfaces:**
- Consumes: GovernancePolicy with user subjects
- Produces: Enforcement key: (team, user, window) for budget/rate

**Priority:** CRITICAL (Week 5, Day 1-2)

- [ ] **Step 1: Write the failing test**

```go
// internal/policy/per_user_test.go
func TestUserBudgetPolicyAccepted(t *testing.T) {
    policy := policy.GovernancePolicy{
        Metadata: policy.Metadata{Name: "user-budget"},
        Spec: policy.Spec{
            Rules: []policy.Rule{{
                Kind:      "budget",
                Subject:   policy.Subject{User: "user123"},
                Budget:    policy.Budget{Limit: 10000}, // $10
            }},
        },
    }
    
    store := policy.NewStore()
    err := store.Load([]policy.GovernancePolicy{policy})
    
    // Should NOT reject user subjects for budget/rate
    assert.NoError(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/policy/... -run TestUserBudgetPolicyAccepted -v`
Expected: FAIL with "user-only subjects not supported for budget/rate"

- [ ] **Step 3: Remove rejection in checkEnforceable**

```go
// internal/policy/store.go
func (s *Store) checkEnforceable(rule Rule) error {
    // BEFORE: Reject user subjects for budget/rate
    // if rule.Kind == "budget" || rule.Kind == "rate" {
    //     if rule.Subject.User != "" {
    //         return UnsupportedError{...}
    //     }
    // }
    
    // AFTER: Support user subjects for budget/rate
    // Governance key: (team, user, window)
    return nil
}
```

- [ ] **Step 4: Add governance key**

```go
// internal/governance/userkey.go
type GovernanceKey struct {
    Team    string
    User    *principal.UserID // Optional, for per-user rules
    Window  string
    Pool    string // For two-pool budgets
}

func (k GovernanceKey) String() string {
    if k.User != nil {
        return fmt.Sprintf("team:%s:user:%s:window:%s:pool:%s", k.Team, k.User, k.Window, k.Pool)
    }
    return fmt.Sprintf("team:%s:window:%s:pool:%s", k.Team, k.Window, k.Pool)
}
```

- [ ] **Step 5: Update PreCheck**

```go
// internal/governance/governance.go
func (g *Governor) PreCheck(ctx context.Context, principal Principal, budgetRule Rule) error {
    // Build governance key
    key := GovernanceKey{
        Team:   principal.Team,
        Window: budgetRule.Budget.Window,
        Pool:   budgetRule.Budget.Pool,
    }
    
    // Add user if per-user budget
    if budgetRule.Subject.User != "" {
        key.User = &principal.UserID
    }
    
    // Check against budget store
    allowance := g.budgetStore.Get(key)
    if g.spend[key] > allowance {
        return ErrBudgetExceeded
    }
    
    return nil
}
```

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/policy/... ./internal/governance/... -v`
Expected: PASS (user budget/rate policies accepted)

- [ ] **Step 7: Commit**

```bash
git add internal/policy/store.go internal/governance/userkey.go internal/governance/governance.go internal/policy/per_user_test.go
git commit -m "feat(governance): support per-user budget/rate enforcement (P0-03 phase 2)

- Remove rejection of user subjects for budget/rate
- Add GovernanceKey with optional User field
- Wire into PreCheck for per-user enforcement

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 6: Per-User Budget Enforcement (P0-03 Phase 2)

**Files:**
- Modify: `internal/budget/budget.go`
- Modify: `internal/governance/governance.go`
- Test: `internal/budget/per_user_budget_test.go`

**Interfaces:**
- Consumes: GovernanceKey with User field from policy
- Produces: Per-user budget counter, keyed by (team, user, window)

**Priority:** CRITICAL (Week 5, Day 3-5)

- [ ] **Step 1: Write the failing test**

```go
// internal/budget/per_user_budget_test.go
func TestPerUserBudgetEnforcement(t *testing.T) {
    store := budget.NewMemory()
    
    userID := principal.UserID{Issuer: "auth.example.com", Sub: "user123"}
    key := governance.GovernanceKey{
        Team:   "demo",
        User:   &userID,
        Window: "2026-08",
        Pool:   "default",
    }
    
    // Set per-user budget: $10
    store.Set(key, 10000000) // 10M microUSD
    
    // Debit $5
    err := store.Debit(key, 5000000)
    require.NoError(t, err)
    
    // Budget exhausted
    err = store.Debit(key, 5000000)
    require.NoError(t, err)
    
    // Over budget should fail
    err = store.Debit(key, 1)
    assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/budget/... -run TestPerUserBudgetEnforcement -v`
Expected: FAIL with "method Debit not defined for GovernanceKey"

- [ ] **Step 3: Add per-user methods to BudgetStore**

```go
// internal/budget/budget.go
type BudgetStore interface {
    // Existing: team-level
    Get(team string) (int64, error)
    Set(team string, allowance int64) error
    Debit(team string, amount int64) error
    
    // New: per-user (via GovernanceKey)
    GetByKey(key governance.GovernanceKey) (int64, error)
    SetByKey(key governance.GovernanceKey, allowance int64) error
    DebitByKey(key governance.GovernanceKey, amount int64) error
}
```

- [ ] **Step 4: Implement methods**

```go
// internal/budget/memory.go
func (m *memoryStore) GetByKey(key governance.GovernanceKey) (int64, error) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    return m.byKey[key.String()], nil
}

func (m *memoryStore) SetByKey(key governance.GovernanceKey, allowance int64) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.byKey[key.String()] = allowance
    return nil
}

func (m *memoryStore) DebitByKey(key governance.GovernanceKey, amount int64) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    
    current := m.byKey[key.String()]
    spent := m.spend[key.String()]
    
    if spent+amount > current {
        return ErrBudgetExceeded
    }
    
    m.spend[key.String()] = spent + amount
    return nil
}
```

- [ ] **Step 5: Update Settle**

```go
// internal/governance/governance.go
func (g *Governor) Settle(ctx context.Context, principal Principal, key GovernanceKey, amount int64) error {
    // Debit from appropriate store
    if key.User != nil {
        return g.budgetStore.DebitByKey(key, amount)
    }
    return g.budgetStore.Debit(key.Team, amount)
}
```

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/budget/... ./internal/governance/... -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/budget/budget.go internal/budget/memory.go internal/governance/governance.go internal/budget/per_user_budget_test.go
git commit -m "feat(budget): implement per-user budget enforcement (P0-03 phase 2)

- Add GetByKey/SetByKey/DebitByKey to BudgetStore
- Wire into Settle for per-user debiting
- GovernanceKey determines team vs user scope

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 7: Role-Based Access Control (P0-04 Phase 1)

**Files:**
- Create: `internal/adminauth/roles.go`
- Create: `internal/adminauth/authz.go`
- Modify: `internal/controlplane/policies.go:170-174`
- Test: `internal/adminauth/roles_test.go`

**Interfaces:**
- Consumes: OIDC identity with role claims
- Produces: Role-based authorization middleware, role definitions

**Priority:** CRITICAL (Week 7, Day 1-2)

- [ ] **Step 1: Write the failing test**

```go
// internal/adminauth/roles_test.go
func TestRequireRoleMiddleware(t *testing.T) {
    // Create mock authorizer
    authz := &mockAuthorizer{allowed: false}
    
    // Create handler with role requirement
    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    
    wrapped := adminauth.RequireRole(adminauth.RolePolicyAdmin).Then(handler)
    
    // Request without role
    req := httptest.NewRequest("GET", "/v1alpha1/policies", nil)
    rec := httptest.NewRecorder()
    
    wrapped.ServeHTTP(rec, req)
    
    assert.Equal(t, http.StatusForbidden, rec.Code)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/adminauth/... -run TestRequireRoleMiddleware -v`
Expected: FAIL with "function not defined"

- [ ] **Step 3: Define roles**

```go
// internal/adminauth/roles.go
package adminauth

type Role string

const (
    RolePlatformAdmin Role = "platform-admin"  // Full access
    RolePolicyAdmin   Role = "policy-admin"    // Policy CRUD
    RoleProviderAdmin Role = "provider-admin"  // Provider/model config
    RoleBudgetAdmin   Role = "budget-admin"    // Budget/quota config
    RoleAuditor       Role = "auditor"         // Read-only audit
    RoleTeamAdmin     Role = "team-admin"      // Team-scoped admin
)

type RoleBinding struct {
    Role   Role
    Scopes []Scope
}

type Scope struct {
    Type   string // "org" | "team"
    TeamID string // If Type == "team"
}
```

- [ ] **Step 4: Implement authorization middleware**

```go
// internal/adminauth/authz.go
package adminauth

import (
    "context"
    "net/http"
)

func RequireRole(role Role) *RoleMiddleware {
    return &RoleMiddleware{role: role}
}

type RoleMiddleware struct {
    role Role
}

func (m *RoleMiddleware) Then(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()
        
        principal := PrincipalFromContext(ctx)
        if principal == nil {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        
        authz := AuthorizerFromContext(ctx)
        if authz == nil {
            http.Error(w, "authorization unavailable", http.StatusInternalServerError)
            return
        }
        
        if err := authz.Authorize(ctx, m.role, principal.Scope); err != nil {
            http.Error(w, "forbidden", http.StatusForbidden)
            return
        }
        
        next.ServeHTTP(w, r)
    })
}
```

- [ ] **Step 5: Update policy routes**

```go
// internal/controlplane/policies.go:170-174
func (s *Server) mountPolicyRoutes(r *mux.Router) {
    // Before: whole-console authority
    // r.HandleFunc("/v1alpha1/policies", s.authn(s.listPolicies)).Methods("GET")
    
    // After: role-based authorization
    r.Handle("/v1alpha1/policies", 
        adminauth.RequireRole(adminauth.RoleAuditor).Then(s.listPolicies),
    ).Methods("GET")
    
    r.Handle("/v1alpha1/policies/{id}",
        adminauth.RequireRole(adminauth.RolePolicyAdmin).Then(s.putPolicy),
    ).Methods("PUT")
    
    r.Handle("/v1alpha1/policies/{id}",
        adminauth.RequireRole(adminauth.RolePolicyAdmin).Then(s.deletePolicy),
    ).Methods("DELETE")
}
```

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/adminauth/... ./internal/controlplane/... -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/adminauth/roles.go internal/adminauth/authz.go internal/controlplane/policies.go internal/adminauth/roles_test.go
git commit -m "feat(authz): add role-based authorization middleware (P0-04 phase 1)

- Define Role constants and RoleBinding
- RequireRole middleware checks role before handler
- Wire into policy routes with proper roles

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 8: Mutation Audit Trail (P0-04 Phase 2)

**Files:**
- Create: `internal/audit/mutation.go`
- Create: `internal/audit/mutation_store.go`
- Test: `internal/audit/mutation_test.go`

**Interfaces:**
- Consumes: Mutation events from policy/provider/config writes
- Produces: MutationRecord with actor/resource/before/after/hash

**Priority:** CRITICAL (Week 8, Day 1-2)

- [ ] **Step 1: Write the failing test**

```go
// internal/audit/mutation_test.go
func TestRecordMutation(t *testing.T) {
    store := audit.NewMutationStore(testDB(t))
    
    record := audit.MutationRecord{
        Actor: audit.Actor{
            UserID: "auth.example.com|user123",
            Roles:  []adminauth.Role{adminauth.RolePolicyAdmin},
        },
        Resource: audit.Resource{
            Type: "policy",
            ID:    "demo-budget",
        },
        Action: audit.Action{
            Operation: "update",
        },
        Before: json.RawMessage(`{"limit":10000}`),
        After:  json.RawMessage(`{"limit":20000}`),
    }
    
    err := store.Record(context.Background(), record)
    require.NoError(t, err)
    
    // Verify retrieval
    records, err := store.Query(context.Background(), audit.QueryFilter{
        UserID: "auth.example.com|user123",
    })
    require.NoError(t, err)
    assert.Len(t, records, 1)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/audit/... -run TestRecordMutation -v`
Expected: FAIL with "type MutationStore not defined"

- [ ] **Step 3: Define MutationRecord**

```go
// internal/audit/mutation.go
package audit

import (
    "encoding/json"
    "time"
    
    "github.com/inferplane/inferplane/internal/adminauth"
    "github.com/inferplane/inferplane/pkg/ulid"
)

type MutationRecord struct {
    ID        ulid.ULID
    Timestamp time.Time
    
    // Who made the change
    Actor Actor
    
    // What was changed
    Resource Resource
    
    // How it was changed
    Action Action
    
    // Before/after state
    Before json.RawMessage
    After  json.RawMessage
    
    // Metadata
    Generation int64
    Hash       string
}

type Actor struct {
    UserID    string
    Issuer    string
    Subject   string
    Roles     []adminauth.Role
}

type Resource struct {
    Type      string // "policy" | "provider" | "config" | "role"
    ID        string
    Team      string
}

type Action struct {
    Operation  string // "create" | "update" | "delete"
    Capability string // "policy:update" etc.
}
```

- [ ] **Step 4: Implement MutationStore**

```go
// internal/audit/mutation_store.go
package audit

import (
    "context"
    "database/sql"
    "time"
    
    "github.com/inferplane/inferplane/pkg/ulid"
)

type MutationStore struct {
    db *sql.DB
}

func NewMutationStore(db *sql.DB) *MutationStore {
    // Ensure schema
    db.Exec(`
        CREATE TABLE IF NOT EXISTS mutation_audit (
            id         TEXT PRIMARY KEY,
            timestamp  INTEGER NOT NULL,
            actor      TEXT NOT NULL,
            resource   TEXT NOT NULL,
            action     TEXT NOT NULL,
            before     TEXT,
            after      TEXT,
            generation INTEGER NOT NULL,
            hash       TEXT NOT NULL
        );
        
        CREATE INDEX idx_mutation_actor ON mutation_audit(actor);
        CREATE INDEX idx_mutation_resource ON mutation_audit(resource);
        CREATE INDEX idx_mutation_timestamp ON mutation_audit(timestamp);
    `)
    
    return &MutationStore{db: db}
}

func (s *MutationStore) Record(ctx context.Context, r MutationRecord) error {
    r.ID = ulid.New()
    r.Timestamp = time.Now()
    
    _, err := s.db.ExecContext(ctx, `
        INSERT INTO mutation_audit (id, timestamp, actor, resource, action, before, after, generation, hash)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, r.ID.String(), r.Timestamp.Unix(), r.Actor.UserID, r.Resource.ID, r.Action.Operation, r.Before, r.After, r.Generation, r.Hash)
    
    return err
}
```

- [ ] **Step 5: Wire into policy write endpoints**

```go
// internal/controlplane/policies.go
func (s *Server) putPolicy(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    
    // Get before state
    before, _ := s.store.Get(r.PathValue("id"))
    
    // ... perform update ...
    
    // Record mutation
    s.audit.Record(ctx, audit.MutationRecord{
        Actor: audit.Actor{
            UserID: principal.UserID.String(),
            Roles:  principal.Roles,
        },
        Resource: audit.Resource{
            Type: "policy",
            ID:    r.PathValue("id"),
        },
        Action: audit.Action{
            Operation: "update",
        },
        Before: beforeJSON,
        After:  afterJSON,
    })
}
```

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/audit/... -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/audit/mutation.go internal/audit/mutation_store.go internal/controlplane/policies.go internal/audit/mutation_test.go
git commit -m "feat(audit): add mutation audit trail (P0-04 phase 2)

- MutationRecord captures actor/resource/action/before/after
- MutationStore persists to SQLite/Postgres
- Wire into policy PUT/DELETE endpoints

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 9: Durable Ledger (P0-05 Phase 1)

**Files:**
- Create: `internal/controlplane/ledger_sqlite.go`
- Modify: `internal/controlplane/controlplane.go:39`
- Test: `internal/controlplane/ledger_test.go`

**Interfaces:**
- Consumes: Lease grants from sync heartbeat
- Produces: Durable ledger surviving control plane restart

**Priority:** CRITICAL (Week 7, Day 1-3)

- [ ] **Step 1: Write the failing test**

```go
// internal/controlplane/ledger_test.go
func TestLedger survivesRestart(t *testing.T) {
    db, _ := sql.Open("sqlite", ":memory:")
    ledger := controlplane.NewSQLiteLedger(db)
    
    // Issue grant
    grant := controlplane.Grant{
        Policy:    "budget",
        Rule:      "demo-budget",
        Team:      "demo",
        Dataplane: "dp-123",
        Window:    "2026-08",
        Allowance: 1000000,
        Spent:     0,
    }
    
    err := ledger.IssueGrant(context.Background(), grant)
    require.NoError(t, err)
    
    // Simulate restart: close and reopen
    db.Close()
    db2, _ := sql.Open("sqlite", ":memory:")
    ledger2 := controlplane.NewSQLiteLedger(db2)
    
    // Verify grant persisted
    grants, err := ledger2.ActiveGrants(context.Background())
    require.NoError(t, err)
    assert.Len(t, grants, 1)
    assert.Equal(t, "demo", grants[0].Team)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controlplane/... -run TestLedgerSurvivesRestart -v`
Expected: FAIL with "type SQLiteLedger not defined"

- [ ] **Step 3: Implement SQLiteLedger**

```go
// internal/controlplane/ledger_sqlite.go
package controlplane

import (
    "context"
    "database/sql"
)

type SQLiteLedger struct {
    db *sql.DB
}

func NewSQLiteLedger(db *sql.DB) *SQLiteLedger {
    // Ensure schema
    db.Exec(`
        CREATE TABLE IF NOT EXISTS ledger (
            policy    TEXT NOT NULL,
            rule      TEXT NOT NULL,
            dataplane TEXT NOT NULL,
            window    TEXT NOT NULL,
            spent     INTEGER NOT NULL,
            allowance INTEGER NOT NULL,
            expires   INTEGER NOT NULL,
            PRIMARY KEY (policy, rule, window, dataplane)
        );
        
        CREATE INDEX idx_ledger_window ON ledger(window);
        CREATE INDEX idx_ledger_expires ON ledger(expires);
    `)
    
    return &SQLiteLedger{db: db}
}

func (l *SQLiteLedger) IssueGrant(ctx context.Context, g Grant) error {
    _, err := l.db.ExecContext(ctx, `
        INSERT INTO ledger (policy, rule, dataplane, window, spent, allowance, expires)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(policy, rule, window, dataplane) DO UPDATE SET
            spent = excluded.spent,
            allowance = excluded.allowance,
            expires = excluded.expires
    `, g.Policy, g.Rule, g.Dataplane, g.Window, g.Spent, g.Allowance, g.Expires)
    
    return err
}

func (l *SQLiteLedger) ActiveGrants(ctx context.Context) ([]Grant, error) {
    rows, err := l.db.QueryContext(ctx, `
        SELECT policy, rule, dataplane, window, spent, allowance, expires
        FROM ledger
        WHERE expires > ?
    `, time.Now().Unix())
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    var grants []Grant
    for rows.Next() {
        var g Grant
        rows.Scan(&g.Policy, &g.Rule, &g.Dataplane, &g.Window, &g.Spent, &g.Allowance, &g.Expires)
        grants = append(grants, g)
    }
    
    return grants, nil
}
```

- [ ] **Step 4: Replace in-memory ledger**

```go
// internal/controlplane/controlplane.go:39
// BEFORE:
type ruleLedger struct {
    grants map[ruleKey]grantState
}

// AFTER:
type Server struct {
    ledger Ledger // Interface, implemented by SQLiteLedger
}

type Ledger interface {
    IssueGrant(ctx context.Context, g Grant) error
    ActiveGrants(ctx context.Context) ([]Grant, error)
    ExpireGrants(ctx context.Context) error
}
```

- [ ] **Step 5: Run all tests**

Run: `go test ./internal/controlplane/... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/controlplane/ledger_sqlite.go internal/controlplane/controlplane.go internal/controlplane/ledger_test.go
git commit -m "feat(controlplane): replace in-memory ledger with SQLite (P0-05 phase 1)

- SQLiteLedger persists grants to survive restart
- Ledger interface for future Postgres backend
- Drop in-memory ruleLedger

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 10: Control-Plane Window Epochs (P0-05 Phase 2)

**Files:**
- Modify: `api/v1alpha1/sync.go`
- Modify: `internal/controlplane/windows.go`
- Test: `internal/controlplane/window_test.go`

**Interfaces:**
- Consumes: Budget rules with window specification
- Produces: WindowID (calendar month UTC) carried in LeaseGrant

**Priority:** CRITICAL (Week 7, Day 4-5)

- [ ] **Step 1: Write the failing test**

```go
// internal/controlplane/window_test.go
func TestComputeWindowIDMonthly(t *testing.T) {
    rule := policy.Rule{
        Budget: policy.Budget{
            Window: "monthly",
        },
    }
    
    // 2026-08-15 UTC → "2026-08"
    windowID := controlplane.ComputeWindowID(rule, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC))
    
    assert.Equal(t, "2026-08", windowID)
}

func TestWindowIDInLeaseGrant(t *testing.T) {
    grant := controlplane.LeaseGrant{
        Team:      "demo",
        Allowance: 1000000,
        WindowID:  "2026-08", // NEW FIELD
    }
    
    assert.NotEmpty(t, grant.WindowID)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controlplane/... -run TestComputeWindowID -v`
Expected: FAIL with "function ComputeWindowID not defined"

- [ ] **Step 3: Add WindowID to LeaseGrant**

```go
// api/v1alpha1/sync.go
type LeaseGrant struct {
    Team      string `json:"team"`
    Allowance int64  `json:"allowance"`
    ExpiresAt int64  `json:"expiresAt"`
    
    // NEW: Window identity (calendar month UTC)
    WindowID  string `json:"windowId"` // "2026-08"
}
```

- [ ] **Step 4: Implement ComputeWindowID**

```go
// internal/controlplane/windows.go
package controlplane

import (
    "time"
)

func ComputeWindowID(rule policy.Rule, now time.Time) string {
    switch rule.Budget.Window {
    case "monthly":
        // Calendar month UTC: "2026-08"
        return now.UTC().Format("2006-01")
    case "daily":
        // Calendar day UTC: "2026-08-15"
        return now.UTC().Format("2006-01-02")
    default:
        // Rolling window (legacy behavior)
        return "rolling"
    }
}
```

- [ ] **Step 5: Wire into sync response**

```go
// internal/controlplane/controlplane.go
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
    // ... existing code ...
    
    // Compute WindowID for each grant
    for i, grant := range grants {
        grants[i].WindowID = ComputeWindowID(rule, time.Now())
    }
    
    // ... send response ...
}
```

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/controlplane/... -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add api/v1alpha1/sync.go internal/controlplane/windows.go internal/controlplane/controlplane.go internal/controlplane/window_test.go
git commit -m "feat(controlplane): add WindowID to lease grants (P0-05 phase 2)

- ComputeWindowID returns calendar month/day UTC
- WindowID carried in LeaseGrant and ConsumptionReport
- Eliminates per-plane window drift

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 11: Rate Shares (P0-05 Phase 3)

**Files:**
- Modify: `api/v1alpha1/sync.go`
- Create: `internal/controlplane/rateshare.go`
- Test: `internal/controlplane/rateshare_test.go`

**Interfaces:**
- Consumes: Active dataplane count, global rate limits
- Produces: RateShare with per-plane RPM/TPM share

**Priority:** CRITICAL (Week 8, Day 1-2)

- [ ] **Step 1: Write the failing test**

```go
// internal/controlplane/rateshare_test.go
func TestRateShareProportionalSplit(t *testing.T) {
    // Global limit: 100 RPM, 4 active planes
    globalRPM := int64(100)
    activePlanes := 4
    
    shares := controlplane.ComputeRateShares(globalRPM, activePlanes)
    
    // Expected: each plane gets 25 RPM (floor ensures new planes can work)
    assert.Equal(t, int64(25), shares[0].RPM)
    assert.LessOrEqual(t, shares[0].RPM, globalRPM/int64(activePlanes))
    
    // Sum of shares ≤ global limit
    var sum int64
    for _, s := range shares {
        sum += s.RPM
    }
    assert.LessOrEqual(t, sum, globalRPM)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controlplane/... -run TestRateShareProportionalSplit -v`
Expected: FAIL with "function ComputeRateShares not defined"

- [ ] **Step 3: Add RateShare type**

```go
// api/v1alpha1/sync.go
type RateShare struct {
    Rule      string `json:"rule"`
    Team      string `json:"team"`
    RPM       int64  `json:"rpm"`
    TPM       int64  `json:"tpm"`
    ExpiresAt int64  `json:"expiresAt"`
}

type SyncResponse struct {
    Leases     []LeaseGrant `json:"leases"`
    Policies   []Policy      `json:"policies"`
    
    // NEW: Rate shares for multi-plane enforcement
    RateShares []RateShare   `json:"rateShares"`
}
```

- [ ] **Step 4: Implement ComputeRateShares**

```go
// internal/controlplane/rateshare.go
package controlplane

func ComputeRateShares(globalRPM, globalTPM int64, activePlanes int) []RateShare {
    if activePlanes == 0 {
        return nil
    }
    
    // Floor ensures new planes can start working
    floorRPM := globalRPM / 10
    floorTPM := globalTPM / 10
    
    // Proportional split with floor
    shareRPM := max(floorRPM, globalRPM/int64(activePlanes))
    shareTPM := max(floorTPM, globalTPM/int64(activePlanes))
    
    var shares []RateShare
    for i := 0; i < activePlanes; i++ {
        shares = append(shares, RateShare{
            RPM:     shareRPM,
            TPM:     shareTPM,
            ExpiresAt: time.Now().Add(30 * time.Second).Unix(),
        })
    }
    
    return shares
}

func max(a, b int64) int64 {
    if a > b {
        return a
    }
    return b
}
```

- [ ] **Step 5: Wire into sync response**

```go
// internal/controlplane/controlplane.go
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
    // ... existing code ...
    
    // Compute rate shares for each rate rule
    var allShares []RateShare
    for _, rule := range s.policies {
        if rule.Kind != "rate" {
            continue
        }
        
        activePlanes := s.activeDataplanes()
        shares := ComputeRateShares(rule.Rate.RPM, rule.Rate.TPM, len(activePlanes))
        
        for i, dp := range activePlanes {
            allShares = append(allShares, RateShare{
                Rule:      rule.Name,
                Team:      rule.Subject.Team,
                RPM:       shares[i].RPM,
                TPM:       shares[i].TPM,
                ExpiresAt: shares[i].ExpiresAt,
            })
        }
    }
    
    response.RateShares = allShares
    
    // ... send response ...
}
```

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/controlplane/... -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add api/v1alpha1/sync.go internal/controlplane/rateshare.go internal/controlplane/controlplane.go internal/controlplane/rateshare_test.go
git commit -m "feat(controlplane): add rate shares for multi-plane enforcement (P0-05 phase 3)

- RateShare carries per-plane RPM/TPM share
- ComputeRateShares splits globally with floor
- Σ shares ≤ global limit invariant

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 12: PII Policy Engine (P0-06 Phase 1)

**Files:**
- Create: `internal/policy/pii.go`
- Create: `internal/governance/egress_ceiling.go`
- Test: `internal/policy/pii_test.go`

**Interfaces:**
- Consumes: DetectionResult from PII detector
- Produces: PIIAction (external-unmodified, external-masked, internal-only, blocked)

**Priority:** CRITICAL (Week 10, Day 1-2)

- [ ] **Step 1: Write the failing test**

```go
// internal/policy/pii_test.go
func TestPIIPolicyDecideNoPII(t *testing.T) {
    engine := policy.NewPIIEngine()
    
    result := policy.DetectionResult{
        HasPII: false,
    }
    
    action := engine.Decide(result, policy.PII{
        DefaultAction: policy.ActionExternalMasked,
    })
    
    // No PII → external-unmodified (detector completed, nothing protected)
    assert.Equal(t, policy.ActionExternalUnmodified, action)
}

func TestPIIPolicyDecidePIIDetected(t *testing.T) {
    engine := policy.NewPIIEngine()
    
    result := policy.DetectionResult{
        HasPII:      true,
        HasSensitive: false,
        Entities: []policy.Entity{
            {Type: "email", Value: "user@example.com"},
        },
    }
    
    action := engine.Decide(result, policy.PII{
        DefaultAction: policy.ActionExternalMasked,
    })
    
    // PII detected → apply default action
    assert.Equal(t, policy.ActionExternalMasked, action)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/policy/... -run TestPIIPolicyDecide -v`
Expected: FAIL with "type PIIEngine not defined"

- [ ] **Step 3: Define PII types**

```go
// internal/policy/pii.go
package policy

import "fmt"

type PIIAction string

const (
    ActionExternalUnmodified PIIAction = "external-unmodified"
    ActionExternalMasked     PIIAction = "external-masked"
    ActionInternalOnly       PIIAction = "internal-only"
    ActionBlocked            PIIAction = "blocked"
)

type DetectionResult struct {
    Entities     []Entity
    HasPII       bool
    HasSensitive bool
}

type Entity struct {
    Type     string // "email" | "ssn" | "credit_card" | "api_key"
    Value    string
    Position Position
}

type PII struct {
    DefaultAction         PIIAction
    FailClosedOnDetection bool
    EntityActions         map[string]PIIAction // Per-entity overrides
}

type PIIEngine struct{}

func NewPIIEngine() *PIIEngine {
    return &PIIEngine{}
}

func (e *PIIEngine) Decide(result DetectionResult, policy PII) PIIAction {
    // Fail-closed: detector error or sensitive PII → block
    if result.HasSensitive && policy.FailClosedOnDetection {
        return ActionBlocked
    }
    
    // No PII detected → external-unmodified (detector chain complete)
    if !result.HasPII {
        return ActionExternalUnmodified
    }
    
    // PII detected → apply policy
    return policy.DefaultAction
}
```

- [ ] **Step 4: Define EgressCeiling**

```go
// internal/governance/egress_ceiling.go
package governance

import (
    "github.com/inferplane/inferplane/internal/policy"
)

type EgressCeiling struct {
    PIIAction        policy.PIIAction
    
    AllowedProviders []string
    AllowedRegions   []string
    GuardrailID      string
}

func ApplyCeiling(ceiling *EgressCeiling, provider, region string) error {
    switch ceiling.PIIAction {
    case policy.ActionBlocked:
        return ErrPIIBlocked
    case policy.ActionInternalOnly:
        if !isInternalProvider(provider) {
            return ErrPIIInternalOnly
        }
    }
    
    // Check provider
    if !contains(ceiling.AllowedProviders, provider) {
        return ErrProviderNotAllowed
    }
    
    // Check region
    if !contains(ceiling.AllowedRegions, region) {
        return ErrRegionNotAllowed
    }
    
    return nil
}
```

- [ ] **Step 5: Run all tests**

Run: `go test ./internal/policy/... ./internal/governance/... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/policy/pii.go internal/governance/egress_ceiling.go internal/policy/pii_test.go
git commit -m "feat(policy): add PII policy engine (P0-06 phase 1)

- PIIAction type with 4 actions
- PIIEngine decides action from detection result
- EgressCeiling attaches per-request policy

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 13: PII Integration (P0-06 Phase 2)

**Files:**
- Modify: `internal/server/anthropicapi/messages.go`
- Modify: `internal/filter/filter.go`
- Modify: `internal/audit/record.go`
- Test: `internal/server/anthropicapi/pii_integration_test.go`

**Interfaces:**
- Consumes: Request body, PII policy from policy store
- Produces: PII decision in audit record, egress ceiling attached

**Priority:** CRITICAL (Week 10, Day 3-5)

- [ ] **Step 1: Write the failing test**

```go
// internal/server/anthropicapi/pii_integration_test.go
func TestPIIEgressCeilingAttached(t *testing.T) {
    // Request with PII (email)
    body := []byte(`{"messages":[{"role":"user","content":"email me at user@example.com"}]}`)
    
    // PII policy
    policy := policy.PII{
        DefaultAction: policy.ActionExternalMasked,
    }
    
    // Detection result
    detection := policy.DetectionResult{
        HasPII: true,
        Entities: []policy.Entity{
            {Type: "email", Value: "user@example.com"},
        },
    }
    
    // Should attach egress ceiling
    ceiling := applyPIIProcessing(body, detection, policy)
    
    assert.Equal(t, policy.ActionExternalMasked, ceiling.PIIAction)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/anthropicapi/... -run TestPIIEgressCeilingAttached -v`
Expected: FAIL with "function applyPIIProcessing not defined"

- [ ] **Step 3: Add PII decision to audit**

```go
// internal/audit/record.go
type Record struct {
    // ... existing fields
    
    // NEW: PII egress decision
    PII *PIIDecision `json:"pii,omitempty"`
}

type PIIDecision struct {
    Action              PIIAction `json:"action"`
    EntitiesDetected    []string   `json:"entities_detected,omitempty"`
    DetectorVersion     string     `json:"detector_version"`
    DetectorChainComplete bool     `json:"detector_chain_complete"`
}
```

- [ ] **Step 4: Integrate into handler**

```go
// internal/server/anthropicapi/messages.go
func (h *Handler) ServeMessages(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    body := readBody(r)
    
    // 1. Detect PII
    detection, detectorErr := h.detector.Detect(ctx, body)
    if detectorErr != nil {
        // Fail-closed: detector error = block
        http.Error(w, "PII detection failed", http.StatusBadGateway)
        return
    }
    
    // 2. Decide action
    piiPolicy := h.policyStore.PIIPolicy()
    action := h.piiEngine.Decide(detection, piiPolicy)
    
    // 3. Attach egress ceiling
    ceiling := &governance.EgressCeiling{
        PIIAction:        action,
        AllowedProviders: h.policy.AllowedProviders,
        AllowedRegions:   h.policy.AllowedRegions,
    }
    ctx = context.WithValue(ctx, egressCeilingKey, ceiling)
    
    // 4. Mask if needed
    if action == policy.ActionExternalMasked {
        body, _ = h.masker.Mask(body, detection.Entities)
    }
    
    // 5. Continue processing (ceiling checked at provider dispatch)
    h.forwardWithCeiling(ctx, w, r, body)
}
```

- [ ] **Step 5: Add to audit record**

```go
// internal/server/anthropicapi/messages.go
auditRecord := audit.Record{
    // ... existing fields
    
    PII: &audit.PIIDecision{
        Action:               action,
        EntitiesDetected:     detection.EntityTypes(),
        DetectorVersion:      h.detector.Version(),
        DetectorChainComplete: detectorErr == nil,
    },
}
```

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/server/anthropicapi/... -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/audit/record.go internal/server/anthropicapi/messages.go internal/server/anthropicapi/pii_integration_test.go
git commit -m "feat(server): integrate PII detection into request path (P0-06 phase 2)

- Detect → Decide → Ceiling flow in message handler
- Add PII decision to audit record
- Fail-closed on detector error

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Self-Review Checklist

After implementing all tasks, verify:

- [ ] **Spec coverage:** All 6 P0 items covered
- [ ] **Placeholder scan:** No TBD/TODO in implementation
- [ ] **Type consistency:** UserID, GovernanceKey, EgressCeiling consistent across tasks
- [ ] **Test coverage:** Each task has tests
- [ ] **Error handling:** Fail-closed on errors (guardrail, detector, detector)

---

## Implementation Notes

**Dependencies between tasks:**
- Tasks 1-2: Independent (Week 1)
- Tasks 3-4: Sequential (identity schema → OIDC extraction)
- Tasks 5-6: Sequential (governance keys → budget enforcement)
- Tasks 7-8: Sequential (RBAC → mutation audit)
- Tasks 9-10-11: Sequential (ledger → window → rate shares)
- Tasks 12-13: Sequential (PII engine → integration)

**Parallel execution opportunities:**
- Week 1: Tasks 1+2 (bugs) in parallel
- Week 7: Start Task 7 (RBAC) + Task 9 (ledger) in parallel
- Week 10: Start Task 12 while Task 11 completes

**Test strategy:**
- Unit tests for each task
- Integration tests after each phase
- End-to-end test after all P0 fixes

**Rollback plan:**
- Each task is independently revertible
- Feature flags: `guardrail_check`, `per_user_budget`, `rate_shares`, `pii_policy`
