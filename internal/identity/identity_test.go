package identity

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIdentityKindsAndExactTuple(t *testing.T) {
	a, err := NewHuman("org", "https://issuer.example", "user-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewHuman("org", "https://other.example", "user-a")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService("org", "build")
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Human || service.Kind != Service || service.Issuer != "inferplane://org/service-account" {
		t.Fatal("typed constructors lost their identity kind")
	}
	if a == b || a.CanonicalRef() == b.CanonicalRef() {
		t.Fatal("equal subjects from different issuers collided")
	}
	changed := a
	changed.Organization = "other"
	if a.CanonicalRef() == changed.CanonicalRef() {
		t.Fatal("organizations collided")
	}
	changed = a
	changed.Issuer += "/"
	if a.CanonicalRef() == changed.CanonicalRef() {
		t.Fatal("issuer was normalized")
	}
	if !strings.HasPrefix(a.CanonicalRef(), "identity-v1:") || len(a.CanonicalRef()) != len("identity-v1:")+64 {
		t.Fatal("canonical reference is not versioned full SHA-256")
	}
}

func TestIdentityValidationDoesNotEchoInput(t *testing.T) {
	good, _ := NewHuman("org", "https://issuer.example", "person")
	for _, tt := range []struct {
		name string
		edit func(*ID)
	}{
		{"empty org", func(i *ID) { i.Organization = "" }},
		{"empty subject", func(i *ID) { i.Subject = "" }},
		{"control", func(i *ID) { i.Subject = "private-value\n" }},
		{"UTF8", func(i *ID) { i.Subject = string([]byte{0xff}) }},
		{"long subject", func(i *ID) { i.Subject = strings.Repeat("x", 2049) }},
		{"http", func(i *ID) { i.Issuer = "http://issuer.example" }},
		{"userinfo", func(i *ID) { i.Issuer = "https://private-value@issuer.example" }},
		{"query", func(i *ID) { i.Issuer = "https://issuer.example?q=private-value" }},
		{"fragment", func(i *ID) { i.Issuer = "https://issuer.example#private-value" }},
		{"unknown kind", func(i *ID) { i.Kind = "other" }},
		{"forged service", func(i *ID) { i.Kind = Service }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := good
			tt.edit(&id)
			if err := id.Validate(); err == nil || strings.Contains(err.Error(), "private-value") {
				t.Fatal("invalid identity accepted or sensitive field echoed")
			}
			if id.CanonicalRef() != "" {
				t.Fatal("invalid identity received an accounting reference")
			}
		})
	}
}

func TestAuditContainsOnlyDigestsAndCanonicalReference(t *testing.T) {
	id, _ := NewHuman("org", "https://private-issuer.example", "private-subject")
	a := id.Audit()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if a.Reference != id.CanonicalRef() || a.Organization != "org" || a.Kind != Human ||
		!strings.HasPrefix(a.IssuerDigest, "sha256:") || !strings.HasPrefix(a.SubjectDigest, "sha256:") ||
		strings.Contains(string(raw), "private-issuer") || strings.Contains(string(raw), "private-subject") {
		t.Fatal("audit representation lost identity or exposed raw identity fields")
	}
}

func TestBindingBijectionAndDeclarationFingerprint(t *testing.T) {
	a, _ := NewHuman("org", "https://issuer.example", "a")
	b, _ := NewService("org", "build")
	first := Config{Organization: "org", Required: true, Bindings: []Binding{{a, "legacy a"}, {b, "legacy-b"}}}
	second := Config{Organization: "org", Required: true, Bindings: []Binding{{b, "legacy-b"}, {a, "legacy a"}}}
	before := append([]Binding(nil), first.Bindings...)
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() == "" || first.Fingerprint() != second.Fingerprint() || !reflect.DeepEqual(before, first.Bindings) {
		t.Fatal("declaration fingerprint depends on order or mutates caller input")
	}
	second.Required = false
	if first.Fingerprint() == second.Fingerprint() {
		t.Fatal("required mode omitted from fingerprint")
	}
	for _, bindings := range [][]Binding{
		{{a, "x"}, {a, "y"}}, {{a, "x"}, {b, "x"}}, {{a, "x"}, {a, "x"}},
		{{a, ""}}, {{a, strings.Repeat("x", 257)}}, {{a, "private-value\n"}},
		{{a, b.CanonicalRef()}}, {{a, "identity-v1:invalid"}},
	} {
		c := Config{Organization: "org", Required: true, Bindings: bindings}
		if c.Validate() == nil || c.Fingerprint() != "" {
			t.Fatal("invalid or ambiguous declaration received a fingerprint")
		}
	}
	if (Binding{a, a.CanonicalRef()}).Validate() != nil {
		t.Fatal("an identity's own canonical reference was rejected")
	}
	if (Binding{a, " "}).Validate() != nil {
		t.Fatal("nonempty legacy reference bytes were normalized into an empty identity")
	}
	if (Config{Required: true}).Validate() == nil {
		t.Fatal("required mode accepted without organization")
	}
	if err := (Config{}).Validate(); err != nil || (Config{}).Fingerprint() != "" {
		t.Fatal("zero configuration must describe absent managed identity")
	}
}
