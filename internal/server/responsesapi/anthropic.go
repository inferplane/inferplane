package responsesapi

import (
	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/pkg/schema"
)

func anthropicRequest(request *schema.ChatRequest) ([]byte, error) {
	return responses.CanonicalToAnthropicRequest(request)
}
