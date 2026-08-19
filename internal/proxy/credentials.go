package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/inferplane/inferplane/providers"
)

// credentialBodyLimit bounds the response read. The body is four short
// strings; 64 KiB is already far more than any legitimate STS triplet needs,
// and the cap keeps a hostile/misconfigured endpoint from streaming an
// unbounded body into memory.
const credentialBodyLimit = 64 << 10

// CredentialFetcher asks the control plane for short-lived upstream
// credentials (ADR-040) and satisfies providers.CredentialSource, so
// providers/bedrock can sign with brokered STS sessions instead of the node's
// own IAM identity.
//
// It is a SEPARATE channel from the sync heartbeat and the usage push, on
// purpose: credentials have a different lifecycle (≤1h vs a 10s cadence) and a
// different security class (a signable secret vs policy documents). It shares
// only the control-plane base URL — the bearer is the DEDICATED broker token,
// never the heartbeat token (ADR-040 decision 1).
//
// SECRET HYGIENE (ADR-040 decision 1, a hard requirement — not an
// implication): no method here logs, prints, or stores a credential field, and
// no returned error ever embeds the response body or any credential value. A
// failure reports the HTTP status code and a fixed string, nothing else. The
// same rule internal/policystore applies to DSN-bearing pgx errors.
type CredentialFetcher struct {
	URL         string // control plane base URL (no trailing slash)
	BrokerToken string // control_plane.broker_token_ref — never the heartbeat token
	Dataplane   string // this data plane's id, for CloudTrail attribution
	Provider    string // upstream provider to broker; empty ⇒ "bedrock"
	client      *http.Client
	mu          sync.Mutex // guards the lazy client build
}

// Credentials fetches one short-lived credential set. The signature is
// providers.CredentialSource: the AWS SDK's CredentialsCache (wired up in
// providers/bedrock) calls it once at construction and then again shortly
// before each expiry, so this is never on the request path.
func (f *CredentialFetcher) Credentials(ctx context.Context) (id, secret, session string, expires time.Time, err error) {
	provider := f.Provider
	if provider == "" {
		provider = "bedrock"
	}
	body, err := json.Marshal(struct {
		Dataplane string `json:"dataplane"`
		Provider  string `json:"provider"`
	}{Dataplane: f.Dataplane, Provider: provider})
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("credential fetch: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL+"/v1alpha1/credentials", bytes.NewReader(body))
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("credential fetch: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if f.BrokerToken != "" {
		req.Header.Set("Authorization", "Bearer "+f.BrokerToken)
	}
	f.mu.Lock()
	if f.client == nil {
		f.client = &http.Client{
			Timeout: 10 * time.Second, // same posture as UsagePusher: a hung POST must not stall the credential cache forever
			// A redirected POST silently becomes a GET, and following a
			// redirect would ship the broker token to whatever host the
			// redirect names — never follow redirects.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	client := f.client
	f.mu.Unlock()
	resp, err := client.Do(req)
	if err != nil {
		// Transport error: %w IS correct here — a *url.Error carries the URL
		// and the network error, no credential material. Contrast the two
		// fixed-string cases below, where the wrapped value could embed
		// response-body bytes.
		return "", "", "", time.Time{}, fmt.Errorf("credential fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// Non-2xx: status code plus a FIXED string only. The body is never
		// read into the error — a broker/proxy error page could echo request
		// or credential material, and resp.Status carries the server's reason
		// phrase, so neither may appear. Drain-and-discard a bounded prefix
		// so the connection can be reused; the bytes are discarded unread on
		// purpose.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, credentialBodyLimit))
		return "", "", "", time.Time{}, fmt.Errorf("credential fetch: control plane rejected the request with HTTP %d", resp.StatusCode)
	}
	var cred struct {
		AccessKeyID     string    `json:"accessKeyId"`
		SecretAccessKey string    `json:"secretAccessKey"`
		SessionToken    string    `json:"sessionToken"`
		Expiration      time.Time `json:"expiration"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, credentialBodyLimit)).Decode(&cred); err != nil {
		// Decode failure: a FIXED string with NO %w. encoding/json's
		// SyntaxError/UnmarshalTypeError text can quote the offending bytes —
		// i.e. the response body, i.e. potentially a credential.
		return "", "", "", time.Time{}, fmt.Errorf("credential fetch: malformed credential response")
	}
	if cred.AccessKeyID == "" || cred.SecretAccessKey == "" || cred.SessionToken == "" || cred.Expiration.IsZero() {
		// Fixed string again: a 200 with an empty/partial body must fail HERE,
		// not later as an unrelated-looking signing error from empty AWS
		// credentials.
		return "", "", "", time.Time{}, fmt.Errorf("credential fetch: credential response is incomplete")
	}
	return cred.AccessKeyID, cred.SecretAccessKey, cred.SessionToken, cred.Expiration, nil
}

var _ providers.CredentialSource = (*CredentialFetcher)(nil)
