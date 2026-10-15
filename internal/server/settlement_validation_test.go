package server

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/server/routingtest"
	"github.com/inferplane/inferplane/pkg/schema"
)

type pendingLimiter struct {
	limiter.LimiterStore
	pending  atomic.Int64
	reserved atomic.Int64
	peak     atomic.Int64
}

func (l *pendingLimiter) ReserveRate(key string, estimate, rate, burst int64) (func(*int64), bool) {
	finish, ok := l.LimiterStore.ReserveRate(key, estimate, rate, burst)
	if !ok {
		return nil, false
	}
	l.reserved.Add(1)
	pending := l.pending.Add(1)
	for peak := l.peak.Load(); pending > peak; peak = l.peak.Load() {
		if l.peak.CompareAndSwap(peak, pending) {
			break
		}
	}
	var once sync.Once
	return func(actual *int64) { once.Do(func() { finish(actual); l.pending.Add(-1) }) }, true
}

func TestEveryIngressClosesAdmissionAcrossRetries(t *testing.T) {
	for _, ing := range authorityIngresses() {
		t.Run(ing.name(), func(t *testing.T) {
			f := routingtest.New(t, func(c *config.Config) {
				m := c.Models["premium"]
				m.Targets = append(m.Targets, config.Target{Provider: "retry", Model: "up"})
				c.Models["premium"] = m
			})
			f.Public.Fail = true
			in, out := int64(2), int64(3)
			f.Retry.Usage = &schema.Usage{InputTokens: &in, OutputTokens: &out}
			l := &pendingLimiter{LimiterStore: limiter.NewMemory()}
			g := governance.NewGovernor(map[string]governance.TeamPolicy{"team": {TokensPerDay: 10000, TokensPerMinute: 1000}}, l, budget.NewMemory(), nil)
			rec := policyDo(policyMux(f, nil, g, nil, nil), ing.path, authorityBody(ing), true)
			if rec.Code != 200 || len(f.Public.Calls) != 1 || len(f.Retry.Calls) != 1 {
				t.Fatalf("status=%d first=%d retry=%d", rec.Code, len(f.Public.Calls), len(f.Retry.Calls))
			}
			wantReservations := int64(1)
			if ing.protocol == "responses" {
				wantReservations = 2
			}
			if l.pending.Load() != 0 || l.peak.Load() != 1 || l.reserved.Load() != wantReservations {
				t.Fatalf("pending=%d peak=%d reservations=%d", l.pending.Load(), l.peak.Load(), l.reserved.Load())
			}
			if got := l.QuotaUsed("quota:team", 24*time.Hour); got != 5 {
				t.Fatalf("terminal usage=%d want 5", got)
			}
		})
	}
}

type accountingPanicWriter struct{ *httptest.ResponseRecorder }

func (accountingPanicWriter) Write([]byte) (int, error) { panic("client write failed") }

func TestEveryIngressClosesAdmissionOnPanic(t *testing.T) {
	for _, ing := range authorityIngresses() {
		t.Run(ing.name(), func(t *testing.T) {
			f := routingtest.New(t, nil)
			l := &pendingLimiter{LimiterStore: limiter.NewMemory()}
			g := governance.NewGovernor(map[string]governance.TeamPolicy{"team": {TokensPerMinute: 1000}}, l, budget.NewMemory(), nil)
			req := httptest.NewRequest("POST", ing.path, strings.NewReader(authorityBody(ing)))
			req.Header.Set("x-api-key", "dev-key")
			var panicked bool
			func() {
				defer func() { panicked = recover() != nil }()
				policyMux(f, nil, g, nil, nil).ServeHTTP(accountingPanicWriter{httptest.NewRecorder()}, req)
			}()
			if !panicked || len(f.Public.Calls) != 1 || l.reserved.Load() != 1 {
				t.Fatal("test did not panic after admission and provider invocation")
			}
			if l.pending.Load() != 0 {
				t.Fatal("panic leaked an admission")
			}
		})
	}
}

func TestEveryIngressValidatesAccountingAndClosesAdmission(t *testing.T) {
	for _, ing := range authorityIngresses() {
		for _, kind := range []string{"negative", "negative-cache", "negative-tier", "overflow", "contradictory-ttl", "partial-ttl", "missing", "failure", "interrupted"} {
			t.Run(ing.name()+"/"+kind, func(t *testing.T) {
				f := routingtest.New(t, func(c *config.Config) {
					c.Pricing.Overrides["public"]["up"] = config.RateConfig{InputPerMTok: 1, OutputPerMTok: 1}
				})
				in, out, flat, five := int64(2), int64(3), int64(100), int64(40)
				u := &schema.Usage{InputTokens: &in, OutputTokens: &out}
				wantQuota, wantCost := int64(900), int64(0)
				switch kind {
				case "negative":
					out = -1000
				case "negative-cache":
					flat = -1
					u.CacheCreationInputTokens = &flat
				case "negative-tier":
					five = -1
					u.CacheCreation = &schema.CacheCreation{Ephemeral5mInputTokens: &five}
				case "overflow":
					in = math.MaxInt64
				case "contradictory-ttl":
					u.CacheCreationInputTokens = &flat
					u.CacheCreation = &schema.CacheCreation{Ephemeral5mInputTokens: &five, Ephemeral1hInputTokens: &five}
				case "partial-ttl":
					u.CacheCreationInputTokens = &flat
					u.CacheCreation = &schema.CacheCreation{Ephemeral5mInputTokens: &five}
					wantQuota, wantCost = 1005, 130
				case "missing":
					u = nil
				case "failure":
					f.Public.Fail = true
				case "interrupted":
					f.Public.Partial = true
					wantQuota, wantCost = 905, 5
				}
				f.Public.Usage = u
				l := &pendingLimiter{LimiterStore: limiter.NewMemory()}
				l.DebitQuota("quota:team", 900, 24*time.Hour)
				b := budget.NewMemory()
				g := governance.NewGovernor(map[string]governance.TeamPolicy{"team": {TokensPerDay: 10000, TokensPerMinute: 1000, BudgetMicrosPerMonth: 100000}}, l, b, nil)
				aud, buf := policyAudit(t)
				closeAudit := sync.OnceFunc(func() {
					if err := aud.Close(); err != nil {
						t.Error(err)
					}
				})
				defer closeAudit()
				_ = policyDo(policyMux(f, aud, g, nil, nil), ing.path, authorityBody(ing), true)
				closeAudit()
				if len(f.Public.Calls) != 1 {
					t.Fatalf("provider was not exercised: %d", len(f.Public.Calls))
				}
				if l.pending.Load() != 0 {
					t.Fatal("request leaked a pending admission")
				}
				if got := l.QuotaUsed("quota:team", 24*time.Hour); got != wantQuota {
					t.Fatalf("quota=%d want %d", got, wantQuota)
				}
				w := budget.CalendarMonthIn(time.UTC)
				if got := b.Spent(budget.Key(budget.ScopeTeam, "team", w), w); got != wantCost {
					t.Fatalf("cost=%d want %d", got, wantCost)
				}
				if kind == "failure" || kind == "missing" {
					return
				}
				var completed *audit.Record
				for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
					var record audit.Record
					if err := json.Unmarshal(line, &record); err != nil {
						t.Fatal(err)
					}
					if record.Event == "request_completed" {
						completed = &record
					}
				}
				if completed == nil || completed.Cost == nil {
					t.Fatalf("missing completion/cost evidence: %s", buf)
				}
				if wantCost == 0 {
					if completed.Usage != nil || !completed.Cost.PricingMissing {
						t.Fatalf("invalid usage recorded as settled: usage=%+v cost=%+v", completed.Usage, completed.Cost)
					}
				} else if completed.Usage == nil || completed.Usage.InputTokens != in || completed.Usage.OutputTokens != out || completed.Cost.PricingMissing {
					t.Fatalf("valid usage lost: usage=%+v cost=%+v", completed.Usage, completed.Cost)
				} else if kind == "partial-ttl" && (completed.Usage.CacheCreationInputTokens != 100 || completed.Usage.CacheCreation5mInputTokens != 100) {
					t.Fatalf("partial TTL total lost: %+v", completed.Usage)
				}
			})
		}
	}
}
