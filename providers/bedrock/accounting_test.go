package bedrock

import (
	"context"
	"errors"
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
			p := &provider{inv: &fakeInvoker{respBody: []byte(body)}}
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
	p := &provider{inv: &fakeInvoker{respBody: []byte(body)}}
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
