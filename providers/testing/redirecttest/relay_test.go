package redirecttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/providers"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type redirectBody struct {
	reads, closes int
	reader        io.Reader
}

func (b *redirectBody) Read(p []byte) (int, error) {
	b.reads++
	if b.reader != nil {
		return b.reader.Read(p)
	}
	return 0, errors.New("synthetic-sensitive-body-read-error")
}

func (b *redirectBody) Close() error {
	b.closes++
	return nil
}

func TestRedirectResponseCannotBeRelayed(t *testing.T) {
	for _, tc := range []struct {
		name, kind, protocol string
		settings             map[string]string
	}{
		{name: "anthropic-api-key", kind: "anthropic", protocol: "anthropic"},
		{name: "anthropic-bearer", kind: "anthropic", protocol: "anthropic", settings: map[string]string{"auth_header": "bearer"}},
		{name: "openai-compatible", kind: "openai_compatible", protocol: "openai"},
	} {
		for _, code := range []int{300, 301, 302, 303, 304, 305, 306, 307, 308, 399} {
			for _, mode := range []string{"complete", "stream", "count"} {
				if mode == "count" && tc.kind != "anthropic" {
					continue
				}
				for _, failRead := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%d/%s/read-error=%t", tc.name, code, mode, failRead), func(t *testing.T) {
						body := &redirectBody{}
						if !failRead {
							body.reader = strings.NewReader("synthetic-sensitive-body with https://redirect.invalid/synthetic-sensitive-location")
						}
						sourceCalls, destinationCalls := 0, 0
						client := &http.Client{
							CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
							Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
								if r.Body != nil {
									r.Body.Close()
								}
								if r.URL.Host != "source.invalid" {
									destinationCalls++
								} else {
									sourceCalls++
								}
								return &http.Response{
									StatusCode: code,
									Header: http.Header{
										"Location":     {"https://redirect.invalid/synthetic-sensitive-location"},
										"Refresh":      {"0; url=https://redirect.invalid/synthetic-sensitive-location"},
										"Set-Cookie":   {"synthetic-sensitive-cookie"},
										"Content-Type": {"text/html"},
									},
									Body: body, Request: r,
								}, nil
							}),
						}
						p, err := providers.New(providers.Config{
							Type: tc.kind, BaseURL: "https://source.invalid", APIKey: "synthetic-test-only",
							Settings: tc.settings, HTTPClient: client,
						})
						if err != nil {
							t.Fatal(err)
						}
						req := &providers.ProxyRequest{
							IngressProtocol: tc.protocol, Model: "m", Upstream: "m",
							RawBody: []byte(`{"model":"m","messages":[{"role":"user","content":"synthetic"}],"max_tokens":16}`),
							Headers: http.Header{}, Stream: mode == "stream",
						}
						switch mode {
						case "complete":
							var response *providers.ProxyResponse
							response, err = p.Complete(context.Background(), req)
							if response != nil {
								t.Error("redirect returned a relayable completion response")
							}
						case "stream":
							seq, streamErr := p.Stream(context.Background(), req)
							err = streamErr
							if seq != nil {
								t.Error("redirect returned a stream")
								for range seq {
								}
							}
						case "count":
							var count int64
							count, err = p.(providers.TokenCounter).CountTokens(context.Background(), req)
							if count != 0 {
								t.Error("redirect supplied a token count")
							}
						}
						var upstream *providers.UpstreamError
						if !errors.As(err, &upstream) || upstream.HTTPStatus() != http.StatusBadGateway {
							t.Errorf("redirect must become a sanitized 502 UpstreamError; got %T", err)
						} else {
							if upstream.Header.Get("Content-Type") != "application/json" || len(upstream.Header) != 1 {
								t.Error("redirect headers were not replaced with static JSON content type")
							}
							if !json.Valid(upstream.Body) || strings.Contains(string(upstream.Body), "synthetic-sensitive") {
								t.Error("redirect body was not replaced with safe JSON")
							}
						}
						if err != nil && strings.Contains(err.Error(), "synthetic-sensitive") {
							t.Error("redirect read failure leaked through the public error")
						}
						if sourceCalls != 1 || destinationCalls != 0 {
							t.Errorf("source_calls=%d destination_calls=%d, want 1/0", sourceCalls, destinationCalls)
						}
						if body.reads != 0 || body.closes != 1 {
							t.Errorf("redirect body reads=%d closes=%d, want 0/1", body.reads, body.closes)
						}
					})
				}
			}
		}
	}
}

var _ io.ReadCloser = (*redirectBody)(nil)
