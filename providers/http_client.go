package providers

import (
	"net/http"
	"time"
)

// NewHTTPClient bounds the default wait for upstream headers without limiting
// the lifetime of an established stream. An injected client retains its
// transport and timeouts (including guarded transports used by admin probes).
// Redirects are refused so credentials stay at the selected destination.
func NewHTTPClient(injected *http.Client) *http.Client {
	client := &http.Client{}
	if injected != nil {
		*client = *injected
	} else {
		tr := &http.Transport{}
		if base, ok := http.DefaultTransport.(*http.Transport); ok {
			tr = base.Clone()
		}
		tr.ResponseHeaderTimeout = 120 * time.Second
		client.Transport = tr
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}
