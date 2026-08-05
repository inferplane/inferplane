package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/inferplane/inferplane/internal/adminauth"
)

// oidcEnv is inferplaned's console-SSO configuration. Unlike mayu, inferplaned
// has no config file (ADR-036 deliberately killed one) — every OIDC setting
// is a fixed env var, the INFERPLANED_TOKEN precedent. Validation rules are
// ported from internal/config's validateOIDC (mayu's equivalent) since
// inferplaned has no config loader to share them with.
type oidcEnv struct {
	Issuer        string
	ClientID      string
	GroupsClaim   string
	AllowedGroups []string
	LoginOrigins  []string
}

// loadOIDCEnv parses the five INFERPLANED_OIDC_* vars via getenv (injected —
// keeps the test hermetic and parallel-safe, no os.Setenv). All five unset
// returns (nil, nil): SSO is entirely absent, callers skip every OIDC seam.
func loadOIDCEnv(getenv func(string) string) (*oidcEnv, error) {
	issuer := getenv("INFERPLANED_OIDC_ISSUER")
	clientID := getenv("INFERPLANED_OIDC_CLIENT_ID")
	groupsClaim := getenv("INFERPLANED_OIDC_GROUPS_CLAIM")
	allowedGroups := splitTrim(getenv("INFERPLANED_OIDC_ALLOWED_GROUPS"))
	loginOrigins := splitTrim(getenv("INFERPLANED_OIDC_LOGIN_ORIGINS"))

	if issuer == "" && clientID == "" && groupsClaim == "" && len(allowedGroups) == 0 && len(loginOrigins) == 0 {
		return nil, nil
	}

	if issuer == "" {
		return nil, fmt.Errorf("inferplaned: INFERPLANED_OIDC_ISSUER is required when any OIDC var is set")
	}
	if clientID == "" {
		return nil, fmt.Errorf("inferplaned: INFERPLANED_OIDC_CLIENT_ID is required (it is the expected token audience)")
	}
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("inferplaned: INFERPLANED_OIDC_ISSUER must be an absolute https URL without query/fragment/userinfo, got %q", issuer)
	}

	seen := map[string]bool{}
	for i, origin := range loginOrigins {
		lu, err := url.Parse(origin)
		if err != nil || lu.Scheme != "https" || lu.Host == "" || lu.RawQuery != "" || lu.Fragment != "" || lu.User != nil {
			return nil, fmt.Errorf("inferplaned: INFERPLANED_OIDC_LOGIN_ORIGINS[%d] must be an absolute https URL without query/fragment/userinfo, got %q", i, origin)
		}
		if lu.Path != "" && lu.Path != "/" {
			return nil, fmt.Errorf("inferplaned: INFERPLANED_OIDC_LOGIN_ORIGINS[%d] must not contain a path, got %q", i, origin)
		}
		if seen[origin] {
			return nil, fmt.Errorf("inferplaned: INFERPLANED_OIDC_LOGIN_ORIGINS[%d] duplicate origin %q", i, origin)
		}
		seen[origin] = true
	}

	if groupsClaim == "" {
		groupsClaim = "groups"
	}

	// Every login would 403 ("identity maps to no team") with no allowed
	// groups — that reads as a broken deploy, so fail at boot instead of
	// letting every operator discover it the hard way.
	if len(allowedGroups) == 0 {
		return nil, fmt.Errorf("inferplaned: INFERPLANED_OIDC_ALLOWED_GROUPS is required when OIDC is configured")
	}

	return &oidcEnv{
		Issuer:        issuer,
		ClientID:      clientID,
		GroupsClaim:   groupsClaim,
		AllowedGroups: allowedGroups,
		LoginOrigins:  loginOrigins,
	}, nil
}

func splitTrim(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// mapping projects the allowed groups onto adminauth.MappingConfig: any
// member of an allowed group gets admin-equivalent (whole-console) access —
// there is no per-team model on the control plane to map into (v1 scope).
func (o *oidcEnv) mapping() adminauth.MappingConfig {
	return adminauth.MappingConfig{AdminGroups: o.AllowedGroups}
}

// connectSrc returns the CSP connect-src widening: nil when the browser flow
// is off (no login origins configured — SSO is API-only, e.g. CLI-minted
// tokens), else the issuer's ORIGIN (scheme+host only — a Cognito issuer
// carries a pool-id path that would break the CSP source expression)
// followed by every configured login origin.
func (o *oidcEnv) connectSrc() []string {
	if len(o.LoginOrigins) == 0 {
		return nil
	}
	u, err := url.Parse(o.Issuer)
	if err != nil {
		return nil
	}
	out := []string{u.Scheme + "://" + u.Host}
	return append(out, o.LoginOrigins...)
}
