package openairesponses

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/providers"
)

type errorTransport func(*http.Request) (*http.Response, error)

func (f errorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeHTTPErrorCodePreservedWithoutMessage(t *testing.T) {
	for _, code := range []string{"context_length_exceeded", "invalid_encrypted_content", "private-value"} {
		for _, stream := range []bool{false, true} {
			client := &http.Client{Transport: errorTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"` + code + `","message":"private-value","param":"private-value"}}`))}, nil
			})}
			p, err := factory(providers.Config{BaseURL: "https://example.invalid/v1", HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			raw := `{"model":"m","stream":false}`
			if stream {
				raw = `{"model":"m","stream":true}`
			}
			req := &providers.ProxyRequest{IngressProtocol: "responses", Upstream: "m", RawBody: []byte(raw)}
			var body []byte
			if stream {
				_, err := p.Stream(context.Background(), req)
				var upstream *providers.UpstreamError
				if !errors.As(err, &upstream) || upstream.HTTPStatus() != 400 {
					t.Fatalf("error=%v", err)
				}
				body = upstream.Body
			} else {
				resp, err := p.Complete(context.Background(), req)
				if err != nil || resp.StatusCode != 400 {
					t.Fatalf("response=%v err=%v", resp, err)
				}
				body = resp.RawBody
			}
			want := code
			if code == "private-value" {
				want = ""
			}
			if providers.ResponseErrorCode(body) != want || strings.Contains(string(body), "private-value") {
				t.Fatalf("unsafe/lost code: %s", body)
			}
		}
	}
}

type limitedErrorReader struct{ read int }

func (r *limitedErrorReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.read += len(p)
	return len(p), nil
}

func TestUpstreamErrorReadIsBounded(t *testing.T) {
	r := &limitedErrorReader{}
	failure := upstreamError(400, nil, r)
	if r.read != (64<<10)+1 {
		t.Fatalf("read=%d", r.read)
	}
	if providers.ResponseErrorCode(failure.Body) != "" {
		t.Fatal("classified invalid error")
	}
}

type failingErrorReader struct{}

func (failingErrorReader) Read([]byte) (int, error) { return 0, errors.New("private-value") }

func TestUntrustedErrorEnvelopesRemainRedacted(t *testing.T) {
	readers := []io.Reader{
		strings.NewReader(`{"error":{"code":"context_length_exceeded"}}` + strings.Repeat(" ", 64<<10)),
		strings.NewReader(`{"error":{"code":"context_length_exceeded"}} trailing-private-value`),
		strings.NewReader(`{"error":{"code":123,"message":"private-value"}}`),
		failingErrorReader{},
	}
	for _, reader := range readers {
		failure := upstreamError(400, nil, reader)
		if providers.ResponseErrorCode(failure.Body) != "" || strings.Contains(string(failure.Body), "private-value") {
			t.Fatalf("unsafe error: %s", failure.Body)
		}
	}
}
