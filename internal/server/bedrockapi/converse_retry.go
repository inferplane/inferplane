package bedrockapi

import (
	"errors"
	"net/http"

	"github.com/inferplane/inferplane/providers"
)

func (h *InvokeHandler) retryError(err error, crossModel bool) bool {
	if !h.converse {
		return true // retain the existing Invoke contract
	}
	var upstream *providers.UpstreamError
	if !errors.As(err, &upstream) {
		return true // transport failure; the filtered policy chain still binds
	}
	status := upstream.HTTPStatus()
	return status >= 500 || status == http.StatusTooManyRequests ||
		(crossModel && isModelNotFound(status, upstream.Body))
}
