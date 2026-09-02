package budget

// Scope names the kind of subject a budget counter belongs to. The value is
// part of the store key, so two scopes must never be able to collide: a team
// name and a user id can look alike, and a team name may legally contain a
// colon (adminapi.validateTeamName bars only "/" and control characters).
const (
	ScopeTeam = "team"
	ScopeKey  = "key"
	ScopeUser = "user"
	// ScopeUserPremium is the PREMIUM pool of a two-pool user budget
	// (Phase 1 spec): same team/user id as ScopeUser, separate counter —
	// premium-model spend debits both.
	ScopeUserPremium = "userprem"
)

// Key builds the BudgetStore key for one (window, scope, id) counter:
//
//	"budget:" + w.Tag() + ":" + scope + ":" + id
//
// e.g. "budget:month:team:acme", "budget:day:team:acme",
// "budget:month:key:ik_abc", "budget:day:user:acme/sub-123".
//
// The window tag comes FIRST, immediately after the fixed "budget:" prefix,
// and that ordering is correctness rather than style. Memory.cur looks a
// bucket up by this string alone and consults the Window only when it creates
// or rolls one, so a daily and a monthly counter sharing a key would share a
// bucket: the daily debit lands in the monthly total and the monthly cap
// resets at midnight. Keeping the tag and the scope at fixed leading offsets
// also means a caller-supplied id can never impersonate another window or
// scope, which the older "budget:"+id form could not promise.
func Key(scope, id string, w Window) string {
	return "budget:" + w.Tag() + ":" + scope + ":" + id
}
