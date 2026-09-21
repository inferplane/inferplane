package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/providers"
)

func TestCompleteRejectsUnsettleableSuccess(t *testing.T) {
	for name, body := range map[string]string{
		"omitted":       `{"content":[{"type":"text","text":"private-upstream-content"}]}`,
		"null_usage":    `{"usage":null,"private":"private-upstream-content"}`,
		"truncated":     `{"private":"private-upstream-content","usage":`,
		"invalid_usage": `{"usage":"private-upstream-content"}`,
		"null_response": `null`,
		"non_json":      `private-upstream-content`,
		"empty":         ``,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := factory(providers.Config{HTTPClient: accountingClient(http.StatusOK, body)})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := p.Complete(context.Background(), &providers.ProxyRequest{
				Model: "m", Upstream: "anthropic.claude-sonnet-4-6-v1:0", RawBody: []byte(`{"messages":[]}`),
			})
			var ue *providers.UpstreamError
			if resp != nil || !errors.As(err, &ue) || ue.StatusCode != http.StatusBadGateway {
				t.Fatalf("unsettleable success: response=%+v error=%v", resp, err)
			}
			if strings.Contains(string(ue.Body), "private-upstream-content") || strings.Contains(err.Error(), "private-upstream-content") {
				t.Fatal("upstream content leaked in synthetic error")
			}
		})
	}
}

func TestCompletePreservesExplicitZeroUsageAndRawBytes(t *testing.T) {
	body := ` {"type":"message","content":[],"usage":{"input_tokens":0,"output_tokens":0}} `
	p, err := factory(providers.Config{HTTPClient: accountingClient(http.StatusOK, body)})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), &providers.ProxyRequest{
		Model: "m", Upstream: "anthropic.claude-sonnet-4-6-v1:0", RawBody: []byte(`{"messages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.RawBody) != body || resp.Parsed == nil || resp.Parsed.Usage == nil || resp.Parsed.Usage.InputTokens == nil || resp.Parsed.Usage.OutputTokens == nil || *resp.Parsed.Usage.InputTokens != 0 || *resp.Parsed.Usage.OutputTokens != 0 {
		t.Fatalf("explicit zero usage/raw bytes changed: %+v", resp)
	}
}

// A transport-only fake exercises Complete without sockets or credentials.
type accountingTransport func(*http.Request) (*http.Response, error)

func (f accountingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func accountingClient(status int, body string) *http.Client {
	return &http.Client{Transport: accountingTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func TestCompletePreservesNonSuccessResponse(t *testing.T) {
	body := `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`
	p, err := factory(providers.Config{HTTPClient: accountingClient(http.StatusServiceUnavailable, body)})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), &providers.ProxyRequest{RawBody: []byte(`{}`)})
	if err != nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable || string(resp.RawBody) != body || resp.Parsed != nil {
		t.Fatalf("upstream error response changed: %+v %v", resp, err)
	}
}
