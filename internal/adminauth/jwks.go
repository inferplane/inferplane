package adminauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// maxJWKSBytes bounds one key-set response; real sets are a few KiB.
const maxJWKSBytes = 1 << 20

// jwksKeySet verifies a token against the keys it already holds first, so a
// token whose kid is cached never touches the network and is never refused
// because another kid failed. Only a token no cached key verifies may refetch
// the issuer's JWKS: fetches are single-flight, concurrent misses wait for the
// same fetch, and a fetch that still left a token unverified (an unknown kid
// or a forged signature) arms a window in which misses fail fast without the
// network. A forged-kid flood therefore costs the IdP at most one fetch per
// window and cannot block a known key; a newly rotated kid is picked up by
// the next fetch the window allows, including one a flood triggers (P2 gate
// r1/r3; measured 2026-10-27).
type jwksKeySet struct {
	url    string
	client *http.Client
	window time.Duration
	now    func() time.Time

	mu       sync.Mutex
	keys     []jose.JSONWebKey
	lastMiss time.Time     // a fetch left a token unverified, or failed
	inflight chan struct{} // closed when the running fetch is settled
}

func newJWKSKeySet(url string, client *http.Client, window time.Duration) *jwksKeySet {
	return &jwksKeySet{url: url, client: client, window: window, now: time.Now}
}

func (s *jwksKeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	// The IDTokenVerifier has already enforced the RS256/ES256 allow-list.
	jws, err := jose.ParseSigned(jwt, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil || len(jws.Signatures) != 1 {
		return nil, errors.New("jwks: malformed token")
	}
	kid := jws.Signatures[0].Header.KeyID
	s.mu.Lock()
	keys := s.keys
	s.mu.Unlock()
	if payload, ok := verifyWith(keys, kid, jws); ok {
		return payload, nil
	}
	return s.verifyAfterFetch(ctx, kid, jws)
}

var errNoKey = errors.New("jwks: no key verifies the token signature")

// verifyAfterFetch handles a cache miss. It waits for a running fetch, fails
// fast inside the miss window, or performs the fetch itself: the fetching
// caller keeps the fetch in flight until it has armed the window, so a
// concurrent miss can never start a second fetch in between. The flight is
// settled in a defer, so even a panic during the fetch releases the waiters
// and arms the window.
func (s *jwksKeySet) verifyAfterFetch(ctx context.Context, kid string, jws *jose.JSONWebSignature) ([]byte, error) {
	s.mu.Lock()
	if done := s.inflight; done != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
		}
		s.mu.Lock()
		keys := s.keys
		s.mu.Unlock()
		if payload, ok := verifyWith(keys, kid, jws); ok {
			return payload, nil
		}
		return nil, errNoKey
	}
	if !s.lastMiss.IsZero() && s.now().Sub(s.lastMiss) < s.window {
		s.mu.Unlock()
		return nil, errors.New("jwks: refetch rate-limited after a recent miss")
	}
	done := make(chan struct{})
	s.inflight = done
	s.mu.Unlock()

	var keys []jose.JSONWebKey
	fetched, ok := false, false
	defer func() {
		s.mu.Lock()
		if fetched {
			s.keys = keys
		}
		if !ok {
			s.lastMiss = s.now() // unknown kid, forged signature, failed fetch or panic
		}
		s.inflight = nil
		s.mu.Unlock()
		close(done)
	}()

	// Detached from ctx: waiters share this fetch, and a canceled request
	// must not abort it for them. The client timeout bounds it.
	keys, err := s.download()
	if err != nil {
		return nil, err
	}
	fetched = true
	payload, ok := verifyWith(keys, kid, jws)
	if !ok {
		return nil, errNoKey
	}
	return payload, nil
}

func verifyWith(keys []jose.JSONWebKey, kid string, jws *jose.JSONWebSignature) ([]byte, bool) {
	for i := range keys {
		if kid != "" && keys[i].KeyID != kid {
			continue
		}
		if payload, err := jws.Verify(&keys[i]); err == nil {
			return payload, true
		}
	}
	return nil, false
}

func (s *jwksKeySet) download() ([]jose.JSONWebKey, error) {
	resp, err := s.client.Get(s.url)
	if err != nil {
		return nil, fmt.Errorf("jwks: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxJWKSBytes))
		return nil, fmt.Errorf("jwks: fetch: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jwks: fetch: %w", err)
	}
	if len(raw) > maxJWKSBytes {
		return nil, errors.New("jwks: key set too large")
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, errors.New("jwks: malformed key set")
	}
	return set.Keys, nil
}
