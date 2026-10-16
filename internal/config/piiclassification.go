package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
)

// PIIClassificationConfig is the opt-in PII classifier block
// (docs/superpowers/specs/2026-10-16-systemone-korean-pii-sample-design.md).
// Absent means no classifier and no overhead. Zero numeric fields take the
// documented defaults through the accessor methods.
type PIIClassificationConfig struct {
	TimeoutMS     int                    `json:"timeout_ms,omitempty"`
	MaxInputBytes int                    `json:"max_input_bytes,omitempty"`
	Cache         *ClassifierCacheConfig `json:"cache,omitempty"`
	Classifiers   []ClassifierConfig     `json:"classifiers"`
}

// ClassifierCacheConfig bounds the verdict cache; max_entries 0 disables it.
type ClassifierCacheConfig struct {
	MaxEntries *int `json:"max_entries,omitempty"`
	TTLSeconds int  `json:"ttl_seconds,omitempty"`
}

// ClassifierConfig is one classifier entry. The token is a reference only.
type ClassifierConfig struct {
	Name         string                     `json:"name"`
	Type         string                     `json:"type"`
	Mode         string                     `json:"mode"`
	Revision     string                     `json:"revision,omitempty"`
	Teams        []string                   `json:"teams,omitempty"`
	Endpoint     string                     `json:"endpoint"`
	DataBoundary string                     `json:"data_boundary"`
	APIKeyRef    *SecretRef                 `json:"api_key_ref,omitempty"`
	Threshold    *float64                   `json:"threshold,omitempty"`
	Options      map[string]json.RawMessage `json:"options,omitempty"`
}

const maxClassifiers = 4

var (
	classifierName     = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	classifierRevision = regexp.MustCompile(`^[a-z0-9._-]{1,32}$`)
)

func (p *PIIClassificationConfig) Timeout() time.Duration {
	if p.TimeoutMS == 0 {
		return 500 * time.Millisecond
	}
	return time.Duration(p.TimeoutMS) * time.Millisecond
}

func (p *PIIClassificationConfig) InputCap() int {
	if p.MaxInputBytes == 0 {
		return 2 << 20
	}
	return p.MaxInputBytes
}

func (p *PIIClassificationConfig) CacheEntries() int {
	if p.Cache == nil || p.Cache.MaxEntries == nil {
		return 100_000
	}
	return *p.Cache.MaxEntries
}

func (p *PIIClassificationConfig) CacheTTL() time.Duration {
	if p.Cache == nil || p.Cache.TTLSeconds == 0 {
		return time.Hour
	}
	return time.Duration(p.Cache.TTLSeconds) * time.Second
}

func (c ClassifierConfig) EffectiveThreshold() float64 {
	if c.Threshold == nil {
		return 0.5
	}
	return *c.Threshold
}

// checkPIIClassificationRaw runs before the structured parse. It rejects an
// inline api_key and any unknown field, so a misspelled key can never silently
// widen a classifier's scope (LoadRaw ignores unknown keys elsewhere).
func checkPIIClassificationRaw(raw json.RawMessage) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var probe struct {
		Classifiers []map[string]json.RawMessage `json:"classifiers"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("config: pii_classification: %w", err)
	}
	for i, c := range probe.Classifiers {
		if _, bad := c["api_key"]; bad {
			return fmt.Errorf("config: pii_classification.classifiers[%d] has inline api_key; use api_key_ref (§7)", i)
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(new(PIIClassificationConfig)); err != nil {
		return fmt.Errorf("config: pii_classification: %w", err)
	}
	return nil
}

func validatePIIClassification(p *PIIClassificationConfig) error {
	if p == nil {
		return nil
	}
	if p.TimeoutMS < 0 || p.TimeoutMS > 30_000 {
		return errors.New("config: pii_classification.timeout_ms must be 1-30000")
	}
	if p.MaxInputBytes != 0 && (p.MaxInputBytes < 1<<10 || p.MaxInputBytes > 16<<20) {
		return errors.New("config: pii_classification.max_input_bytes must be 1024-16777216")
	}
	if p.Cache != nil {
		if m := p.Cache.MaxEntries; m != nil && (*m < 0 || *m > 1_000_000) {
			return errors.New("config: pii_classification.cache.max_entries must be 0-1000000")
		}
		if t := p.Cache.TTLSeconds; t != 0 && (t < 60 || t > 86_400) {
			return errors.New("config: pii_classification.cache.ttl_seconds must be 60-86400")
		}
	}
	if len(p.Classifiers) == 0 {
		return errors.New("config: pii_classification.classifiers must list at least one classifier")
	}
	if len(p.Classifiers) > maxClassifiers {
		return fmt.Errorf("config: pii_classification allows at most %d classifiers", maxClassifiers)
	}
	seen := make(map[string]bool, len(p.Classifiers))
	for i, c := range p.Classifiers {
		at := fmt.Sprintf("config: pii_classification.classifiers[%d]", i)
		switch {
		case !classifierName.MatchString(c.Name):
			return fmt.Errorf("%s: name must match [a-z0-9-]{1,32}", at)
		case seen[c.Name]:
			return fmt.Errorf("%s: duplicate name %q", at, c.Name)
		case c.Type == "":
			return fmt.Errorf("%s: type is required", at)
		case c.Revision != "" && !classifierRevision.MatchString(c.Revision):
			return fmt.Errorf("%s: revision must match [a-z0-9._-]{1,32}", at)
		case c.Mode != "Shadow" && c.Mode != "Enforce":
			return fmt.Errorf("%s: mode must be Shadow or Enforce", at)
		case c.DataBoundary == "external":
			return fmt.Errorf("%s: external classifiers are not supported in this version", at)
		case c.DataBoundary != "internal":
			return fmt.Errorf("%s: data_boundary must be internal", at)
		case c.Threshold != nil && !(*c.Threshold > 0 && *c.Threshold < 1):
			return fmt.Errorf("%s: threshold must be in (0,1)", at)
		}
		seen[c.Name] = true
		if err := validateClassifierEndpoint(c.Endpoint); err != nil {
			return fmt.Errorf("%s: %w", at, err)
		}
		if err := ValidateSecretRef(c.APIKeyRef); err != nil {
			return fmt.Errorf("%s.api_key_ref: %w", at, err)
		}
		for _, team := range c.Teams {
			if team == "" {
				return fmt.Errorf("%s: teams entries must be non-empty", at)
			}
		}
	}
	return nil
}

// validateClassifierEndpoint never echoes the URL: it may carry a credential.
func validateClassifierEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("endpoint must be an absolute http(s) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("endpoint must not carry userinfo, a query or a fragment")
	}
	return nil
}
