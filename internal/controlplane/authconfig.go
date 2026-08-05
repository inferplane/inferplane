package controlplane

import (
	"encoding/json"
	"net/http"
)

// AuthConfigView is the secret-free bootstrap payload the usage console SPA
// reads to decide whether to show the SSO button and, if so, which public
// OAuth2 identifiers to use (mirrors internal/server's AuthConfigView,
// ADR-026). It never carries a secret — issuer and client_id are the public
// identifiers of a PKCE public client.
type AuthConfigView struct {
	SSO      bool   `json:"sso"`
	Issuer   string `json:"issuer,omitempty"`
	ClientID string `json:"client_id,omitempty"`
}

// AuthConfigHandler serves GET /ui/auth/config, unauthenticated (the console
// shell itself is data-free and served unauthenticated too — see
// internal/controlplane/ui). cfg is nil when OIDC is unconfigured, in which
// case the route 404s rather than mounting a permanent {sso:false} — main.go
// only calls this when there's something to report.
func AuthConfigHandler(cfg func() *AuthConfigView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := cfg()
		if v == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}
