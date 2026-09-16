package policy

import (
	"encoding/hex"
	"fmt"
	"strings"

	v1alpha1 "github.com/inferplane/inferplane/api/v1alpha1"
	"github.com/inferplane/inferplane/internal/identity"
)

// ValidateIdentitySubjects validates references without rewriting subjects or
// accounting keys. Canonical references are enrolled from verified credentials;
// legacy references require the explicit, shared migration declaration.
func ValidateIdentitySubjects(docs []v1alpha1.GovernancePolicy, cfg *identity.Config) error {
	if cfg == nil || !cfg.Required {
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("identity policy configuration: %w", err)
	}
	for _, doc := range docs {
		if err := validateIdentitySubject(doc.Spec.Subject.User, cfg); err != nil {
			return err
		}
	}
	return nil
}

func validateIdentitySubject(ref string, cfg *identity.Config) error {
	if cfg == nil || !cfg.Required || ref == "" {
		return nil
	}
	for _, binding := range cfg.Bindings {
		if ref == binding.AccountRef {
			return nil
		}
		if ref == binding.Identity.CanonicalRef() {
			return fmt.Errorf("identity policy: mapped identity must retain its existing account reference")
		}
	}
	if suffix, ok := strings.CutPrefix(ref, "identity-v1:"); ok && len(suffix) == 64 && strings.ToLower(suffix) == suffix {
		if _, err := hex.DecodeString(suffix); err == nil {
			return nil
		}
	}
	return fmt.Errorf("identity policy: user reference is not canonical or explicitly bound")
}

// SetIdentityConfig is a startup-only declaration, like SetSharedEnforcement.
// Hot reload cannot change a credential store's identity namespace or mappings.
func (s *Store) SetIdentityConfig(cfg *identity.Config) error {
	if cfg != nil {
		if err := cfg.Validate(); err != nil {
			return err
		}
	}
	for _, p := range s.Policies() {
		if err := validateIdentitySubject(p.Subject.User, cfg); err != nil {
			return err
		}
	}
	s.identityConfig = cfg
	return nil
}
