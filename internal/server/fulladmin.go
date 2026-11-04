package server

import (
	"net/http"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
)

// WithFullAdminRoutes layers extra routes in front of mayu's AdminMux without
// growing its positional signature — the WithEvaluations pattern, used by the
// observability console (sub-project B). Every route runs behind AdminAuth and
// requireAdmin, reads included, so a team-mapped identity gets 403 and an
// admin_denied record. Anything else, including an unregistered method on a
// registered path, falls through to admin. No routes leaves admin unchanged.
func WithFullAdminRoutes(admin http.Handler, routes map[string]http.Handler, tokens []string, verifier OIDCVerifier, mapping adminauth.MappingConfig, aud *audit.Writer) http.Handler {
	if len(routes) == 0 {
		return admin
	}
	var emit func(audit.Record)
	if aud != nil {
		emit = aud.Append
	}
	denied := adminDenialEmitter(emit)
	mux := http.NewServeMux()
	for pattern, h := range routes {
		mux.Handle(pattern, AdminAuth(tokens, verifier, mapping, denied, requireAdmin(h, emit)))
	}
	mux.Handle("/", admin)
	return mux
}
