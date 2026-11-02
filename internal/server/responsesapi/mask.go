package responsesapi

import (
	"context"

	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
)

// legacyMask runs the legacy text filter through the complete redactor and
// re-reads the result. A redaction error and an unreadable result are the
// same refusal, so the caller writes one mask_failed denial for either.
func legacyMask(ctx context.Context, r router.RequestRedactor, raw []byte) ([]byte, *schema.ChatRequest, error) {
	sanitized, err := r.Redact(ctx, "responses", raw)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := responses.RequestToCanonical(sanitized)
	if err != nil {
		return nil, nil, err
	}
	return sanitized, parsed, nil
}
