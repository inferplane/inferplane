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
	Usage *usage `json:"usage"`
}

// usage is the server's report on whether it read the whole window. laya-serve
// cuts a state to its token budget without an error and reports the cut here;
// a strict Jev server reports neither field. A field of the wrong type fails
// the JSON decode, so a malformed report is a decode error.
type usage struct {
	Truncated          *bool `json:"truncated"`
	StateTokensDropped *int  `json:"state_tokens_dropped"`
}

// coverage is how much of a window the server says it read.
type coverage int

const (
	readWhole  coverage = iota // the server read the whole window
	readCut                    // the server reports it cut the window
	unreported                 // truncation "reported", and the result does not say
)

// reading is the server's answer for one window.
type reading struct {
	p        float64
	coverage coverage
}

// read checks one result. The gateway never estimates token counts: whether a
// window was read whole comes only from the server's usage report, or from the
// operator's "rejected" assertion that the server refuses over-length input.
func (a answer) read(truncation string) (reading, bool) {
	ans, ok := a.Answers[questionID]
	if !ok || ans.Type != "noul" || ans.Noul == nil {
		return reading{}, false
	}
	p := *ans.Noul
	if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
		return reading{}, false
	}
	r := reading{p: p, coverage: readWhole}
	u := a.Usage
	if u != nil && u.StateTokensDropped != nil && *u.StateTokensDropped < 0 {
		return reading{}, false
	}
	switch {
	case truncation == truncationReported && (u == nil || u.Truncated == nil):
		r.coverage = unreported
	case u != nil && ((u.Truncated != nil && *u.Truncated) || (u.StateTokensDropped != nil && *u.StateTokensDropped > 0)):
		r.coverage = readCut
	}
	return r, true
}

// score returns one reading per window, in order.
func (c *systemone) score(ctx context.Context, windows []string) ([]reading, error) {
	q := map[string]question{questionID: {Type: "noul", Instructions: c.s.instructions, Criteria: c.s.criteria}}
	if c.s.batch {
		body := map[string]any{"states": windows, "questions": q}
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
		out := make([]reading, len(windows))
		for i, a := range resp.Results {
			r, ok := a.read(c.s.truncation)
			if !ok {
				return nil, classifier.ErrDecode
			}
			out[i] = r
		}
		return out, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make([]reading, len(windows))
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
			body := map[string]any{"state": windows[i], "questions": q}
			if c.s.model != "" {
				body["model"] = c.s.model
			}
			var a answer
			if err := c.post(ctx, "/v1/systemone", body, &a); err != nil {
				fail(err)
				return
			}
			r, ok := a.read(c.s.truncation)
			if !ok {
				fail(classifier.ErrDecode)
				return
			}
			out[i] = r
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
