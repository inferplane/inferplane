package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/inferplane/inferplane/internal/telemetry"
)

// usageBufferCap bounds the pusher's retry FIFO: 60 one-minute windows ≈ 1h
// of control-plane outage tolerance. This FIFO is the SINGLE retry store of
// the telemetry pipeline — the control plane acks a batch only after it is
// durable (or memory-accepted in memory-only mode), so nothing here may be
// discarded silently.
const usageBufferCap = 60

// UsagePusher drains the Collector every interval and POSTs each window
// batch to the control plane's /v1alpha1/usage. It never touches the request
// path: its own goroutine, its own bounded buffer, its own HTTP client.
//
// Failure classification (P2 gate): 4xx = permanent (the batch will never be
// accepted — drop it and count it) EXCEPT 408/429, which an LB/proxy emits
// for transient conditions; 5xx / network errors = retryable (the control
// plane returns 503 while its durable store is down — this FIFO is the retry
// leg that makes that safe). The whole backlog flushes oldest-first per tick,
// stopping at the first retryable failure, so it actually drains after an
// outage.
type UsagePusher struct {
	URL       string // control plane base URL (no trailing slash)
	Token     string // bearer; empty for loopback control planes
	Collector *telemetry.Collector
	Interval  time.Duration    // drain cadence; 0 → 60s
	OnError   func(error)      // optional error sink (log)
	OnDrop    func()           // optional drop hook (metrics counter)
	Now       func() time.Time // injectable clock; nil → time.Now
	client    *http.Client     // built on first use
	buf       []*telemetry.UsageBatch
}

// Run drains and flushes until ctx is done. Same lifecycle posture as
// Syncer.Run: started once from the gateway when control_plane is configured.
func (p *UsagePusher) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Final best-effort flush so a clean shutdown ships the last
			// window instead of dropping it (bounded by the client timeout).
			p.Tick(context.Background())
			return
		case <-t.C:
			p.Tick(ctx)
		}
	}
}

// Tick performs one drain-and-flush cycle (exported seam for tests and the
// shutdown path).
func (p *UsagePusher) Tick(ctx context.Context) {
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	if b := p.Collector.Drain(now); b != nil {
		if len(p.buf) >= usageBufferCap {
			// Overflow: drop the OLDEST (freshest data is most valuable) —
			// loudly, never silently.
			p.buf = p.buf[1:]
			p.drop(fmt.Errorf("proxy: usage buffer full — dropped the oldest window"))
		}
		p.buf = append(p.buf, b)
	}
	// Flush the whole backlog oldest-first; stop at the first RETRYABLE
	// failure (a permanent rejection drops that batch and continues, so one
	// poison batch can never head-of-line-block the rest).
	for len(p.buf) > 0 {
		err, permanent := p.post(ctx, p.buf[0])
		if err == nil {
			p.buf = p.buf[1:]
			continue
		}
		if permanent {
			p.buf = p.buf[1:]
			p.drop(fmt.Errorf("proxy: usage batch permanently rejected: %w", err))
			continue
		}
		if p.OnError != nil {
			p.OnError(err)
		}
		return // retryable — keep the backlog for the next tick
	}
}

func (p *UsagePusher) drop(err error) {
	if p.OnDrop != nil {
		p.OnDrop()
	}
	if p.OnError != nil {
		p.OnError(err)
	}
}

// post sends one batch. The bool reports a PERMANENT failure (4xx except
// 408/429): retrying it can never succeed.
func (p *UsagePusher) post(ctx context.Context, b *telemetry.UsageBatch) (error, bool) {
	if p.client == nil {
		p.client = &http.Client{
			Timeout: 10 * time.Second, // same posture as the Syncer: a hung POST must not stall future drains
			// A redirected POST silently becomes a GET whose 200 would
			// discard an unpersisted batch — never follow redirects.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	body, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("usage push: encode: %w", err), true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+"/v1alpha1/usage", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("usage push: %w", err), true
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("usage push: %w", err), false
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode/100 == 2:
		return nil, false
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("usage push: transient HTTP %d", resp.StatusCode), false
	case resp.StatusCode/100 == 4, resp.StatusCode/100 == 3:
		// 3xx counts as permanent too: we never follow redirects, and a
		// misconfigured URL retried forever would wedge the FIFO.
		return fmt.Errorf("usage push: rejected with HTTP %d", resp.StatusCode), true
	default:
		return fmt.Errorf("usage push: HTTP %d", resp.StatusCode), false
	}
}
