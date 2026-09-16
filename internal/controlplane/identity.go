package controlplane

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/identity"
	"github.com/inferplane/inferplane/internal/policy"
)

func (s *Server) identityMachineAuthorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	token := strings.TrimPrefix(header, "Bearer ")
	return s.token != "" && strings.HasPrefix(header, "Bearer ") &&
		!adminauth.IsOIDCBearerShape(token) &&
		subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1
}

// SetIdentityConfig installs an immutable startup declaration. The authority
// database independently enforces the same fingerprint during admission.
func (s *Server) SetIdentityConfig(cfg *identity.Config) error {
	if cfg != nil {
		if err := cfg.Validate(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := policy.ValidateIdentitySubjects(s.wire, cfg); err != nil {
		return err
	}
	s.identityConfig = cfg
	return nil
}

func (s *Server) identityFingerprint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identityConfig == nil || !s.identityConfig.Required {
		return ""
	}
	return s.identityConfig.Fingerprint()
}
