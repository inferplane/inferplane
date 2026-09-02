package adminauth

// Duty-separation roles (Phase 0b-3, design spec
// docs/specs/2026-09-02-durable-identity-and-management-authz.md §4): a
// FIXED set — no custom authorization language (strategy non-goal). Roles
// are granted from the same verified OIDC groups claim team mapping already
// uses; the static admin token remains platform-admin break-glass.
//
// Role gating is OPT-IN: with no RoleMappings configured, every
// authenticated identity keeps its pre-roles authority, byte-identical.

// The fixed role set.
const (
	RolePlatformAdmin = "platform-admin" // everything, including role config
	RolePolicyAdmin   = "policy-admin"   // GovernancePolicy writes (control plane)
	RoleProviderAdmin = "provider-admin" // provider/model topology + probe writes
	RoleBudgetAdmin   = "budget-admin"   // team governance records (budgets/limits)
	RoleAuditor       = "auditor"        // audit/log/debug reads only
	RoleTeamAdmin     = "team-admin"     // keys + team records, within entitled teams
)

// ValidRole reports whether name is one of the fixed roles — config
// validation rejects anything else so a typo'd role fails the load, never
// silently grants nothing.
func ValidRole(name string) bool {
	switch name {
	case RolePlatformAdmin, RolePolicyAdmin, RoleProviderAdmin, RoleBudgetAdmin, RoleAuditor, RoleTeamAdmin:
		return true
	}
	return false
}

// RoleMapping maps a single IdP group to fixed roles.
type RoleMapping struct {
	Group string
	Roles []string
}

// ResolveRoles maps a verified identity's groups onto roles: the
// deduplicated union of all matching mappings ("*" matches any identity
// with at least one group, the GroupMappings convention). An empty result
// under an ACTIVE role config means the identity holds no management
// capability beyond authenticated reads — deliberate, never a default
// grant.
func ResolveRoles(groups []string, cfg MappingConfig) []string {
	if len(cfg.RoleMappings) == 0 || len(groups) == 0 {
		return nil
	}
	inGroups := map[string]bool{}
	for _, g := range groups {
		inGroups[g] = true
	}
	var roles []string
	seen := map[string]bool{}
	for _, m := range cfg.RoleMappings {
		if m.Group != "*" && !inGroups[m.Group] {
			continue
		}
		for _, role := range m.Roles {
			if !seen[role] {
				seen[role] = true
				roles = append(roles, role)
			}
		}
	}
	return roles
}
