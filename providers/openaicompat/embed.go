package openaicompat

// Embeddings lane (roadmap ⑤): the OPTIONAL providers.Embedder capability.
// This provider's native wire IS the OpenAI wire, so the passthrough is the
// trivial case: the client's /v1/embeddings body forwards byte-preserving,
// only the top-level model rewritten to the upstream id (rewriteModel — the
// same order-preserving splice the chat path uses). usage.prompt_tokens is
// parsed for settle; the body itself is never re-serialized.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/inferplane/inferplane/providers"
)

// Embed implements providers.Embedder.
func (p *provider) Embed(ctx context.Context, req *providers.EmbedRequest) (*providers.EmbedResponse, error) {
	body, err := rewriteModel(req.RawBody, req.Upstream)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: embed body: %w", err)
	}
	u, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	u.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		u.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(u)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: embed upstream call: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: embed read upstream: %w", err)
	}
	out := &providers.EmbedResponse{StatusCode: resp.StatusCode, Headers: resp.Header, RawBody: raw}
	if resp.StatusCode/100 == 2 {
		var parsed struct {
			Usage struct {
				PromptTokens int64 `json:"prompt_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &parsed) == nil {
			out.PromptTokens = parsed.Usage.PromptTokens
		}
	}
	return out, nil
}

var _ providers.Embedder = (*provider)(nil)
