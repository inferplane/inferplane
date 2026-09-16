# Provider redirect boundary implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans`.
> Steps use checkbox syntax. Implementation is recorded below; repository-wide
> and latest-HEAD PR gates remain mandatory.

**Goal:** Prevent an upstream HTTP redirect from bypassing the destination chosen
by routing in the Anthropic and Chat Completions providers.

**Architecture:** Copy the configured HTTP client by value and override
`CheckRedirect`, following the existing native Responses provider. Keep caller
transport, timeout and cookie jar intact; never mutate the caller-owned client.
Also replace generation/count 3xx responses with static JSON 502 errors so ingress
cannot relay Location or the redirect body to a client. No shared core change or
new runtime abstraction is needed.

**Tech Stack:** Go 1.25, `net/http`, `httptest`, existing provider registry.

**Spec:** [Hardening program, work package A](2026-09-16-enterprise-hardening-program.md).
The approved destination constrains the provider attempt; a redirect must not
grant authority to a new endpoint.

## Global constraints

- Tests use only loopback fake servers and synthetic credentials.
- No prompt or upstream credential may reach a redirect target.
- Preserve normal passthrough, streaming, count, health and usage contracts.
- Runtime changes remain under `providers/`; assembled HTTP acceptance lives
  under `cmd/mayu/`. No `internal/*` implementation edit.
- Ingress count APIs still return 200 under their existing local-fallback contract.
- Run required checks and retain DCO/latest-HEAD review requirements.

## Evidence and scope

A local two-server reproduction against `6b5cf9c` on 2026-09-16 used one
Complete request per provider and a 307 response from the configured endpoint.
Both `anthropic` and `openai_compatible` contacted the redirect destination with
the body and a synthetic gateway credential. `openai_responses` contacted no
redirect destination. This demonstrates transport behavior, not exploitation of
a live endpoint. Other redirect codes and operations still need the tests below.

| File | Responsibility |
|---|---|
| `providers/anthropic/anthropic.go` | Apply the non-following client policy in `factory` |
| `providers/openaicompat/openaicompat.go` | Apply the same policy in `factory` |
| `providers/testing/redirecttest/redirect_test.go` (new) | Public-registry cross-provider regression matrix |
| `providers/testing/redirecttest/relay_test.go` (new) | Reject client-relayable responses, sensitive headers/bodies and read failures |
| `providers/anthropic/anthropic_test.go` | Caller-owned client preservation |
| `providers/openaicompat/openaicompat_test.go` | Caller-owned client preservation |
| `cmd/mayu/provider_redirect_test.go` (new) | Real HTTP count endpoint returns 200 after upstream redirects |

## Task 1: Add the failing destination matrix

**Interfaces consumed:** `providers.New(Config)`, `Provider.Complete/Stream`,
`providers.TokenCounter`, `providers.HealthChecker`, `ProxyRequest`.
**Produces:** regression tests; no new production API.

- [ ] Create `providers/testing/redirecttest/redirect_test.go` with:

```go
package redirecttest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inferplane/inferplane/providers"
	_ "github.com/inferplane/inferplane/providers/anthropic"
	_ "github.com/inferplane/inferplane/providers/openaicompat"
	_ "github.com/inferplane/inferplane/providers/openairesponses"
)

func TestRedirectNeverLeavesSelectedDestination(t *testing.T) {
	for _, kind := range []string{"anthropic", "openai_compatible", "openai_responses"} {
		for _, code := range []int{301, 302, 303, 307, 308} {
			for _, mode := range []string{"complete", "stream", "count", "health"} {
				if mode == "count" && kind != "anthropic" ||
					mode == "health" && kind == "openai_responses" {
					continue // These providers do not implement this operation.
				}
				for _, injected := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%d/%s/injected=%t", kind, code, mode, injected), func(t *testing.T) {
						var reached, sourceCalls atomic.Int64
						target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							reached.Add(1)
							_, _ = io.Copy(io.Discard, r.Body)
							w.WriteHeader(http.StatusBadGateway)
						}))
						defer target.Close()
						source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							sourceCalls.Add(1)
							http.Redirect(w, r, target.URL, code)
						}))
						defer source.Close()
						cfg := providers.Config{
							Type: kind, BaseURL: source.URL, APIKey: "synthetic-test-only",
						}
						if injected {
							cfg.HTTPClient = source.Client()
							cfg.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
						}
						p, err := providers.New(cfg)
						if err != nil { t.Fatal(err) }
						protocol := "openai"
						if kind == "anthropic" { protocol = "anthropic" }
						if kind == "openai_responses" { protocol = "responses" }
						raw := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"synthetic"}],"input":"synthetic","stream":false}`
						if kind == "openai_responses" {
							raw = `{"model":"m","input":"synthetic","stream":false}`
						}
						if mode == "stream" { raw = strings.Replace(raw, `"stream":false`, `"stream":true`, 1) }
						req := &providers.ProxyRequest{
							IngressProtocol: protocol, Model: "m", Upstream: "m",
							RawBody: []byte(raw), Headers: http.Header{}, Stream: mode == "stream",
						}
						switch mode {
						case "complete":
							_, _ = p.Complete(context.Background(), req)
						case "stream":
							seq, err := p.Stream(context.Background(), req)
							if err == nil {
								for _, streamErr := range seq {
									if streamErr != nil { break }
								}
							}
						case "count":
							_, _ = p.(providers.TokenCounter).CountTokens(context.Background(), req)
						case "health":
							_ = p.(providers.HealthChecker).HealthCheck(context.Background())
						}
						if sourceCalls.Load() != 1 {
							t.Fatal("test did not reach the configured endpoint exactly once")
						}
						if reached.Load() != 0 {
							t.Fatal("redirect reached an endpoint outside the selected destination")
						}
					})
				}
			}
		}
	}
}
```

- [ ] Run `go test ./providers/testing/redirecttest -run TestRedirect -count=1`.
  Expect destination-contact failures in the two legacy providers and passing
  native Responses cases. A compile error is not the expected red test.
- [ ] Add the same assertion for Anthropic `Settings["auth_header"]="bearer"`:
  use a fourth matrix entry with a separate case name, `Type: "anthropic"` and
  `Settings: map[string]string{"auth_header": "bearer"}`. The API-key variant
  above remains in the matrix.

## Task 2: Pin both providers to the selected destination

**Consumes:** the existing `providers.Config.HTTPClient`.
**Produces:** the same provider interfaces with redirect following disabled.

- [ ] In each of the two `factory` functions replace client initialization with:

```go
client := http.Client{}
if cfg.HTTPClient != nil {
	client = *cfg.HTTPClient
}
client.CheckRedirect = func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
```

- [ ] Set the returned provider's `client` field to `&client`. Keep existing
  base URL normalization, API key and Anthropic bearer selection unchanged.
- [ ] Refuse every received 3xx before reading or forwarding its body in
  Complete/Stream and Anthropic CountTokens. Close that body exactly once;
  return the package-local `redirectError()` with status 502, static JSON and
  only a JSON Content-Type header. Strip Location, Refresh, cookies and raw
  redirect/read-error text. Health probes already return sanitized status-only
  failures. `ErrUseLastResponse` alone is insufficient because ingress header
  passthrough could otherwise redirect the original client request.
- [ ] In each provider's existing test package add a factory test that passes an
  `http.Client` with a transport, timeout and permissive redirect callback.
  Assert `p.(*provider).client != original`, equal Transport/Timeout/Jar,
  original callback still returns nil, and copied callback returns
  `http.ErrUseLastResponse`. The required assertions are:

```go
jar, err := cookiejar.New(nil)
if err != nil { t.Fatal(err) }
original := &http.Client{
	Transport: http.DefaultTransport,
	Timeout: time.Second,
	Jar: jar,
	CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
}
p, err := factory(providers.Config{BaseURL: "http://127.0.0.1", HTTPClient: original})
if err != nil { t.Fatal(err) }
actual := p.(*provider).client
if actual == original || actual.Transport != original.Transport ||
	actual.Timeout != original.Timeout || actual.Jar != original.Jar {
	t.Fatal("caller-owned client or settings changed")
}
if original.CheckRedirect(nil, nil) != nil ||
	actual.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
	t.Fatal("redirect policy was not isolated")
}
```

  Add `time` and `net/http/cookiejar` to each test file's imports. Use
  `TestFactoryCopiesClientAndDisablesRedirects` in both package-local files.
- [ ] Run `go test ./providers/anthropic ./providers/openaicompat
  ./providers/openairesponses ./providers/testing/redirecttest -race`.
  Existing passthrough, count, health and usage tests must remain green.

## Task 3: Verify integration and prepare the fix PR

- [ ] Run both static builds, `go test ./... -race`, `go vet ./...`,
  `gofmt -l .`, and `bash tests/run-all.sh`. Use a disposable Postgres DSN to
  include the authority suites; do not call live providers.
- [ ] Verify count-refusal tests still return HTTP 200 and fallback attempts
  still require their own admission. Treat a non-2xx upstream redirect as an
  upstream failure under the existing ingress contract.
- [ ] Run `git diff --check`. The runtime diff must contain no core changes,
  injected-client mutation, permissive redirect exception or secret value.
- [ ] Commit the two factory changes and tests with:

```bash
git add providers/anthropic providers/openaicompat providers/testing/redirecttest
git commit -s -m "fix(providers): refuse redirects beyond selected destinations"
```

- [ ] Follow latest-HEAD AI review and required CI through merge under the
  standing PR workflow. A merged fix still requires a separate deployment to
  change an already-running gateway.

## Implementation record

The provider changes and both regression matrices are now in this branch. The
destination matrix covers 130 combinations, including Anthropic bearer mode.
The relay matrix adds 160 cases over all selected 3xx classes and failing-body
reads. Client preservation uses a nonnil cookie jar. Full integration and remote
PR checks determine release status; the earlier `6b5cf9c` reproduction remains
historical evidence.

The assembled count regression adds ten real HTTP cases (five redirect codes,
API-key/bearer authentication). The client retains normal redirect following;
the test requires zero destination calls and a local positive `input_tokens`
estimate with HTTP 200 and no leaked redirect headers/body.
