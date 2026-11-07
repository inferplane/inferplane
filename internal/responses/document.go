package responses

import (
	"encoding/json"
	"sync"

	"github.com/inferplane/inferplane/pkg/schema"
)

// ParsedRequest is a request-local, validated observation of one immutable
// body. Reparse after masking; never reuse it for different bytes. Native
// forwarding continues to use the original body, not this observation.
type ParsedRequest struct {
	fields          map[string]json.RawMessage
	canonical       *schema.ChatRequest
	conversionOnce  sync.Once
	conversionError error
	strictOnce      sync.Once
	strict          bool
	strictError     error
}

func ParseRequest(raw []byte) (*ParsedRequest, error) {
	fields, err := object(raw)
	if err != nil {
		return nil, err
	}
	canonical, err := requestToCanonical(fields)
	if err != nil {
		return nil, err
	}
	return &ParsedRequest{fields: fields, canonical: canonical}, nil
}

// Canonical returns the observation. Callers must not mutate it.
func (p *ParsedRequest) Canonical() *schema.ChatRequest { return p.canonical }
func (p *ParsedRequest) StoredReference() bool          { return storedReference(p.fields) }

func (p *ParsedRequest) ValidateConversion() error {
	p.conversionOnce.Do(func() {
		p.conversionError = validateConversion(p.fields, p.canonical)
	})
	return p.conversionError
}

func (p *ParsedRequest) StrictTools() (bool, error) {
	p.strictOnce.Do(func() { p.strict, p.strictError = strictTools(p.fields) })
	return p.strict, p.strictError
}
