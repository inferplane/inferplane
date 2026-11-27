package requestpolicy

import (
	"testing"

	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/server/routingtest"
)

// The gateway skips its own estimate-based refusal only when no target of
// the chain could receive the request without an upstream that decides
// overflow exactly; one ordinary target keeps the declared window.
func TestLocalContextWindowDefersOnlyToAnAllAuthorityChain(t *testing.T) {
	f := routingtest.New(t, nil)
	f.Public.ContextAuthority = true
	st := f.Holder.Load()
	authority := router.ChainTarget{Provider: f.Public, ProviderName: "public", Model: "premium", Upstream: "up"}
	ordinary := router.ChainTarget{Provider: f.Private, ProviderName: "private", Model: "premium", Upstream: "up"}
	for name, tc := range map[string]struct {
		chain []router.ChainTarget
		want  int64
	}{
		"authority only": {[]router.ChainTarget{authority}, 0},
		"mixed":          {[]router.ChainTarget{authority, ordinary}, 100000},
		"ordinary only":  {[]router.ChainTarget{ordinary}, 100000},
		"empty chain":    {nil, 100000},
	} {
		if got := LocalContextWindow(st, "premium", tc.chain); got != tc.want {
			t.Errorf("%s: LocalContextWindow = %d, want %d", name, got, tc.want)
		}
	}
}
