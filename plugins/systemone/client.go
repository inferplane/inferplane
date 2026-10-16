package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sync"

	"github.com/inferplane/inferplane/internal/classifier"
)

type question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type answer struct {
	Answers map[string]struct {
		Type string   `json:"type"`
		Noul *float64 `json:"noul"`
	} `json:"answers"`
}

func (a answer) probability() (float64, bool) {
	ans, ok := a.Answers[questionID]
	if !ok || ans.Type != "noul" || ans.Noul == nil {
		return 0, false
	}
	p := *ans.Noul
	if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
		return 0, false
	}
	return p, true
}

// score returns one probability per window, in order.
func (c *systemone) score(ctx context.Context, windows []window) ([]float64, error) {
	q := map[string]question{questionID: {Type: "noul", Instructions: c.s.instructions, Criteria: c.s.criteria}}
	if c.s.batch {
		states := make([]string, len(windows))
		for i, w := range windows {
			states[i] = w.text
		}
		body := map[string]any{"states": states, "questions": q}
		if c.s.model != "" {
			body["model"] = c.s.model
		}
		var resp struct {
			Results []answer `json:"results"`
		}
		if err := c.post(ctx, "/v1/systemone/batch", body, &resp); err != nil {
			return nil, err
		}
		if len(resp.Results) != len(windows) {
			return nil, classifier.ErrDecode
		}
		out := make([]float64, len(windows))
		for i, a := range resp.Results {
			p, ok := a.probability()
			if !ok {
				return nil, classifier.ErrDecode
			}
			out[i] = p
		}
		return out, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make([]float64, len(windows))
	sem := make(chan struct{}, c.s.concurrency)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	fail := func(err error) { once.Do(func() { first = err; cancel() }) }
	for i := range windows {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				fail(classifier.ErrTimeout)
				return
			}
			defer func() { <-sem }()
			body := map[string]any{"state": windows[i].text, "questions": q}
			if c.s.model != "" {
				body["model"] = c.s.model
			}
			var a answer
			if err := c.post(ctx, "/v1/systemone", body, &a); err != nil {
				fail(err)
				return
			}
			p, ok := a.probability()
			if !ok {
				fail(classifier.ErrDecode)
				return
			}
			out[i] = p
		}(i)
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	return out, nil
}

// post sends one JSON request and decodes a bounded JSON response. Every
// failure maps to a bounded error class.
func (c *systemone) post(ctx context.Context, path string, body, into any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return classifier.ErrDecode
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return classifier.ErrTransport
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return classifier.ErrTimeout
		}
		return classifier.ErrTransport
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return classifier.ErrTimeout
		}
		return classifier.ErrTransport
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return classifier.ErrStatus
	}
	if len(raw) > maxResponseBytes || json.Unmarshal(raw, into) != nil {
		return classifier.ErrDecode
	}
	return nil
}
