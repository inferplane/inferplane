package live

import (
	"fmt"
	"log"

	"github.com/inferplane/inferplane/internal/classifier"
	"github.com/inferplane/inferplane/internal/config"
)

// buildClassifiers resolves the opt-in classifier block into this
// generation's Set, with a fresh verdict cache. nil config means no Set.
func buildClassifiers(p *config.PIIClassificationConfig) (*classifier.Set, error) {
	if p == nil {
		return nil, nil
	}
	entries := make([]classifier.Entry, 0, len(p.Classifiers))
	for _, cc := range p.Classifiers {
		token, err := config.ResolveSecretRef(cc.APIKeyRef)
		if err != nil {
			return nil, fmt.Errorf("live: classifier %q api_key_ref: %w", cc.Name, err)
		}
		if cc.APIKeyRef != nil && token == "" {
			// The env path already rejects an empty value; a whitespace-only
			// token file must not silently run without a bearer either.
			return nil, fmt.Errorf("live: classifier %q api_key_ref resolved to an empty token", cc.Name)
		}
		cfg := classifier.Config{Name: cc.Name, Type: cc.Type, Revision: cc.Revision, Endpoint: cc.Endpoint,
			BearerToken: token, Threshold: cc.EffectiveThreshold(), Options: cc.Options}
		c, err := classifier.New(cfg)
		if err != nil {
			return nil, fmt.Errorf("live: %w", err)
		}
		e := classifier.Entry{Name: cc.Name, Revision: cc.Revision, Mode: classifier.Mode(cc.Mode), Classifier: c,
			Global: len(cc.Teams) == 0, Settings: classifier.SettingsDigest(cfg)}
		scope := "all teams"
		if !e.Global {
			e.Teams = make(map[string]bool, len(cc.Teams))
			for _, t := range cc.Teams {
				e.Teams[t] = true
			}
			scope = fmt.Sprintf("teams %v", cc.Teams)
		}
		entries = append(entries, e)
		log.Printf("inferplane: WARNING: PII classifier %q (%s, %s) sends request text to %s", cc.Name, cc.Mode, scope, cc.Endpoint)
	}
	return classifier.NewSet(entries, p.Timeout(), p.InputCap(), p.CacheEntries(), p.CacheTTL())
}

// Classifiers returns this generation's PII classifier Set; nil when none.
func (s *State) Classifiers() *classifier.Set {
	if s == nil {
		return nil
	}
	return s.classifiers
}
