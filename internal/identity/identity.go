// Package identity defines verified identity attribution and immutable accounting
// bindings. Constructors validate syntax; they do not authenticate a caller.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Kind string

const (
	Human   Kind = "human"
	Service Kind = "service"
)

type ID struct {
	Organization string `json:"organization"`
	Kind         Kind   `json:"kind"`
	Issuer       string `json:"issuer"`
	Subject      string `json:"subject"`
}

type Binding struct {
	Identity   ID     `json:"identity"`
	AccountRef string `json:"account_ref"`
}

type Config struct {
	Organization string    `json:"organization"`
	Required     bool      `json:"required"`
	Bindings     []Binding `json:"bindings,omitempty"`
}

// AuditRef deliberately omits raw issuer, subject and legacy account references.
type AuditRef struct {
	Reference     string `json:"reference"`
	Organization  string `json:"organization"`
	Kind          Kind   `json:"kind"`
	IssuerDigest  string `json:"issuer_digest"`
	SubjectDigest string `json:"subject_digest"`
}

var ErrInvalid = errors.New("identity: invalid identity or binding declaration")

func validText(s string, limit int) bool {
	if s == "" || strings.TrimSpace(s) == "" || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func NewHuman(org, verifiedIssuer, subject string) (ID, error) {
	id := ID{org, Human, verifiedIssuer, subject}
	return id, id.Validate()
}

func NewService(org, serviceID string) (ID, error) {
	id := ID{org, Service, "inferplane://" + org + "/service-account", serviceID}
	return id, id.Validate()
}

func (id ID) Validate() error {
	if !validText(id.Organization, 256) || !validText(id.Issuer, 2048) || !validText(id.Subject, 2048) {
		return ErrInvalid
	}
	switch id.Kind {
	case Human:
		u, err := url.Parse(id.Issuer)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" ||
			u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
			strings.Contains(id.Issuer, "#") {
			return ErrInvalid
		}
	case Service:
		if id.Issuer != "inferplane://"+id.Organization+"/service-account" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CanonicalRef hashes a domain prefix followed by a framed, exact identity tuple.
// Invalid identities have no usable reference.
func (id ID) CanonicalRef() string {
	if id.Validate() != nil {
		return ""
	}
	raw, _ := json.Marshal([]string{id.Organization, string(id.Kind), id.Issuer, id.Subject})
	return "identity-v1:" + digest(append([]byte("inferplane.identity.v1\x00"), raw...))
}

func (id ID) Audit() AuditRef {
	if id.Validate() != nil {
		return AuditRef{}
	}
	return AuditRef{
		Reference: id.CanonicalRef(), Organization: id.Organization, Kind: id.Kind,
		IssuerDigest: "sha256:" + digest([]byte(id.Issuer)), SubjectDigest: "sha256:" + digest([]byte(id.Subject)),
	}
}

func (b Binding) Validate() error {
	if b.Identity.Validate() != nil || b.AccountRef == "" || len(b.AccountRef) > 256 || !utf8.ValidString(b.AccountRef) ||
		(strings.HasPrefix(b.AccountRef, "identity-v1:") && b.AccountRef != b.Identity.CanonicalRef()) {
		return ErrInvalid
	}
	for _, c := range b.AccountRef {
		if unicode.IsControl(c) {
			return ErrInvalid
		}
	}
	return nil
}

// Validate accepts the zero configuration as an absent managed-identity setting.
func (c Config) Validate() error {
	if c.Organization == "" && !c.Required && len(c.Bindings) == 0 {
		return nil
	}
	if !validText(c.Organization, 256) {
		return ErrInvalid
	}
	ids, refs := map[ID]bool{}, map[string]bool{}
	for _, b := range c.Bindings {
		if b.Validate() != nil || b.Identity.Organization != c.Organization || ids[b.Identity] || refs[b.AccountRef] {
			return ErrInvalid
		}
		ids[b.Identity], refs[b.AccountRef] = true, true
	}
	return nil
}

// Fingerprint covers only the configured declaration, not dynamic enrollment.
// Nil and empty binding lists are equivalent; the caller's slice is not mutated.
func (c Config) Fingerprint() string {
	if c.Validate() != nil || c.Organization == "" {
		return ""
	}
	c.Bindings = slices.Clone(c.Bindings)
	slices.SortFunc(c.Bindings, func(a, b Binding) int {
		if n := strings.Compare(a.Identity.CanonicalRef(), b.Identity.CanonicalRef()); n != 0 {
			return n
		}
		return strings.Compare(a.AccountRef, b.AccountRef)
	})
	raw, _ := json.Marshal(c)
	return "identity-config-v1:" + digest(append([]byte("inferplane.identity.config.v1\x00"), raw...))
}
