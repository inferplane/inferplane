package requestpolicy

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/inferplane/inferplane/internal/budget"
	"github.com/inferplane/inferplane/internal/config"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/limiter"
	"github.com/inferplane/inferplane/internal/live"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/pkg/schema"
)

type localTestAuthority struct {
	reserveErr error
	permit     *governance.BudgetPermit
	actual     *int64
	complete   bool
	finishes   int
}

func (a *localTestAuthority) Reserve(context.Context, governance.Subject, int64) (*governance.BudgetPermit, error) {
	return a.permit, a.reserveErr
}

func (a *localTestAuthority) Finish(_ context.Context, _ *governance.BudgetPermit, actual *int64, complete bool) error {
	a.actual, a.complete = actual, complete
	a.finishes++
	return nil
}

func TestReserveErrorReturnsOnlyUndispatchedTPM(t *testing.T) {
	for _, cause := range []string{"multiplicity", "missing-state", "missing-route", "authority-refusal", "missing-permit", "missing-bound"} {
		t.Run(cause, func(t *testing.T) {
			_, p, target, state := localBudgetFixture(100, 1000)
			l := limiter.NewMemory()
			g := governance.NewGovernor(map[string]governance.TeamPolicy{
				"team": {BudgetMicrosPerMonth: 100, BudgetExceeded: "block", TokensPerMinute: 1000},
			}, l, budget.NewMemory(), nil)
			raw := []byte(`{"model":"m"}`)
			switch cause {
			case "multiplicity":
				raw = []byte(`{"n":2}`)
			case "missing-state":
				state = nil
			case "missing-route":
				target.Model = "missing"
			case "authority-refusal":
				g.SetBudgetAuthority(&localTestAuthority{reserveErr: governance.ErrAuthorityExhausted})
			case "missing-permit":
				g.SetBudgetAuthority(&localTestAuthority{})
			case "missing-bound":
				g.SetBudgetAuthority(&localTestAuthority{})
				_, _, _, state = localBudgetFixture(100, 0)
			}
			dec := g.PreCheck(governance.Subject{Team: "team", KeyID: "key"}, governance.KeyPolicy{}, 10)
			if !dec.Allowed {
				t.Fatalf("precheck: %+v", dec)
			}
			_, _, err := ReserveBudget(httptest.NewRequest("POST", "/v1/messages", nil), g, p, target, state, raw, dec.Admission)
			if err == nil {
				t.Fatal("expected reservation error")
			}
			dec.Admission.Close()
			if used := l.RateUsed("tpm:team", 1000, 1000); used != 0 {
				t.Fatalf("undispatched reservation failure retained TPM=%d", used)
			}
			if held := g.UsageOf(governance.Subject{Team: "team"}, governance.KeyPolicy{}).TeamBudget.ReservedUSDMicros; held != 0 {
				t.Fatalf("undispatched authority failure retained local money=%d", held)
			}
		})
	}
}

func TestLocalPartialPriceCannotBecomeDurableActual(t *testing.T) {
	g, p, target, state := localBudgetFixture(100, 1000)
	authority := &localTestAuthority{permit: &governance.BudgetPermit{ID: "p", BoundMicroUSD: 2000}}
	g.SetBudgetAuthority(authority)
	reserved, finish, err := ReserveBudget(httptest.NewRequest("POST", "/v1/messages", nil), g, p, target, state, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	in := int64(10)
	SettleBudget(reserved, nil, &schema.Usage{InputTokens: &in}, false)
	finish()
	if authority.finishes != 1 || authority.actual != nil || authority.complete {
		t.Fatalf("partial local price fabricated durable cost/refund: %+v", authority)
	}
	if got := g.UsageOf(governance.Subject{Team: "team"}, governance.KeyPolicy{}).TeamBudget; got.SpentUSDMicros != 10 || got.ReservedUSDMicros != 2000 {
		t.Fatalf("local observation or uncertainty lost: %+v", got)
	}
}

func TestLocalRetrySharesAdmissionButDebitsEachAttempt(t *testing.T) {
	g, p, target, state := localBudgetFixture(1000, 1000)
	p.BudgetUSDMicrosPerDay = 1000
	kp := governance.KeyPolicy{BudgetMicrosPerDay: 1000}
	subject := governance.Subject{Team: p.Team, KeyID: p.KeyID}
	admission := g.PreCheck(subject, kp, 1).Admission
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	in, out := int64(10), int64(1)
	usage := &schema.Usage{InputTokens: &in, OutputTokens: &out}
	for range 2 {
		reserved, finish, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`), admission)
		if err != nil {
			t.Fatal(err)
		}
		g.Settle(subject, kp, "p", "up", pricing.FromUsage(usage), state.Pricing(), admission)
		SettleBudget(reserved, nil, usage, true)
		finish()
		finish() // finalizers must not repeat debit
	}
	got := g.UsageOf(subject, kp)
	if got.TeamBudget.SpentUSDMicros != 22 || got.KeyBudgetDay.SpentUSDMicros != 22 ||
		got.TeamBudget.ReservedUSDMicros != 0 || got.KeyBudgetDay.ReservedUSDMicros != 0 {
		t.Fatalf("attempts were suppressed or double-debited by shared admission: %+v", got)
	}
}

func TestLocalPartialUnknownTTLIsNotPricedAsFiveMinutes(t *testing.T) {
	g, p, target, state := localBudgetFixture(1000, 1000)
	table := pricing.New(pricing.OnMissingBlock, map[pricing.Key]pricing.Rate{
		{Provider: "p", Model: "up"}: {InputPerMTok: 1_000_000, OutputPerMTok: 1_000_000, CacheWrite5mPerMTok: 2_000_000, CacheWrite1hPerMTok: 3_000_000},
	})
	state = live.NewState(nil, map[string]config.ModelConfig{"m": {ContextWindow: 1000}}, table, nil)
	reserved, finish, err := ReserveBudget(httptest.NewRequest("POST", "/v1/messages", nil), g, p, target, state, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	in, out, total := int64(10), int64(2), int64(20)
	SettleBudget(reserved, nil, &schema.Usage{InputTokens: &in, OutputTokens: &out, CacheCreationInputTokens: &total}, false)
	finish()
	if got := g.UsageOf(governance.Subject{Team: "team"}, governance.KeyPolicy{}).TeamBudget; got.SpentUSDMicros != 12 || got.ReservedUSDMicros != 7000 {
		t.Fatalf("unknown TTL was priced or uncertainty released: %+v", got)
	}
}

func TestLocalPartialPriceNeedsKnownContextTier(t *testing.T) {
	g, p, target, state := localBudgetFixture(1000, 1000)
	table := pricing.New(pricing.OnMissingBlock, map[pricing.Key]pricing.Rate{
		{Provider: "p", Model: "up"}: {InputPerMTok: 1_000_000, OutputPerMTok: 2_000_000, ShortContextLimit: 100,
			LongContext: &pricing.Rate{InputPerMTok: 500_000, OutputPerMTok: 500_000}},
	})
	state = live.NewState(nil, map[string]config.ModelConfig{"m": {ContextWindow: 1000}}, table, nil)
	reserved, finish, err := ReserveBudget(httptest.NewRequest("POST", "/v1/messages", nil), g, p, target, state, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	out := int64(10)
	SettleBudget(reserved, nil, &schema.Usage{OutputTokens: &out}, false)
	finish()
	if got := g.UsageOf(governance.Subject{Team: "team"}, governance.KeyPolicy{}).TeamBudget; got.SpentUSDMicros != 0 || got.ReservedUSDMicros != 3000 {
		t.Fatalf("unknown input selected a fabricated context price: %+v", got)
	}
}

var _ governance.BudgetAuthority = (*localTestAuthority)(nil)

func TestLocalMoneyRefusalReturnsUndispatchedTPM(t *testing.T) {
	_, p, target, state := localBudgetFixture(100, 1000)
	l := limiter.NewMemory()
	g := governance.NewGovernor(map[string]governance.TeamPolicy{
		"team": {BudgetMicrosPerMonth: 100, BudgetExceeded: "block", TokensPerMinute: 1000},
	}, l, budget.NewMemory(), nil)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	_, finish, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	dec := g.PreCheck(governance.Subject{Team: "team", KeyID: "key"}, governance.KeyPolicy{}, 10)
	if !dec.Allowed {
		t.Fatalf("precheck: %+v", dec)
	}
	_, _, err = ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`), dec.Admission)
	if BudgetStatus(err) != 402 {
		t.Fatalf("want monetary refusal: %v", err)
	}
	dec.Admission.Close()
	if got := l.RateUsed("tpm:team", 1000, 1000); got != 0 {
		t.Fatalf("undispatched monetary refusal retained TPM=%d", got)
	}
}

func TestLocalRetryRefusalRetainsPreviouslyDispatchedTPM(t *testing.T) {
	_, p, target, state := localBudgetFixture(100, 1000)
	l := limiter.NewMemory()
	g := governance.NewGovernor(map[string]governance.TeamPolicy{
		"team": {BudgetMicrosPerMonth: 100, BudgetExceeded: "block", TokensPerMinute: 1000},
	}, l, budget.NewMemory(), nil)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	dec := g.PreCheck(governance.Subject{Team: "team", KeyID: "key"}, governance.KeyPolicy{}, 10)
	if !dec.Allowed {
		t.Fatalf("precheck: %+v", dec)
	}
	_, finish, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`), dec.Admission)
	if err != nil {
		t.Fatal(err)
	}
	finish() // dispatched first attempt returned no usage
	_, _, err = ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`), dec.Admission)
	if BudgetStatus(err) != 402 {
		t.Fatalf("want retry monetary refusal: %v", err)
	}
	dec.Admission.Close()
	if got := l.RateUsed("tpm:team", 1000, 1000); got == 0 {
		t.Fatal("retry refusal refunded TPM of the earlier dispatched attempt")
	}
}

func TestLocalPartialUsageRecordsKnownPriceWithoutRefund(t *testing.T) {
	for _, mode := range []string{"block", "warn"} {
		t.Run(mode, func(t *testing.T) {
			g, p, target, state := localBudgetFixture(100, 1000)
			g.SetTeamLookup(func(string) (governance.TeamPolicy, bool) {
				return governance.TeamPolicy{BudgetMicrosPerMonth: 100, BudgetExceeded: mode}, true
			})
			subject := governance.Subject{Team: "team", KeyID: "key"}
			admission := g.PreCheck(subject, governance.KeyPolicy{}, 1).Admission
			req := httptest.NewRequest("POST", "/v1/messages", nil)
			reserved, finish, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`), admission)
			if err != nil {
				t.Fatal(err)
			}
			in := int64(10)
			usage := &schema.Usage{InputTokens: &in}
			g.Settle(subject, governance.KeyPolicy{}, "p", "up", pricing.FromUsage(usage), state.Pricing(), admission)
			SettleBudget(reserved, nil, usage, false)
			finish()
			got := g.UsageOf(subject, governance.KeyPolicy{}).TeamBudget
			wantHeld := int64(0)
			if mode == "block" {
				wantHeld = 2000
			}
			if got.SpentUSDMicros != 10 || got.ReservedUSDMicros != wantHeld {
				t.Fatalf("known price lost or uncertain liability refunded: spent=%d reserved=%d", got.SpentUSDMicros, got.ReservedUSDMicros)
			}
		})
	}
}

func TestLocalInvalidCategoryKeepsKnownPriceAndFreezes(t *testing.T) {
	g, p, target, state := localBudgetFixture(100, 1000)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	reserved, finish, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	in, out, bad := int64(10), int64(2), int64(-1)
	SettleBudget(reserved, nil, &schema.Usage{InputTokens: &in, OutputTokens: &out, CacheReadInputTokens: &bad}, true)
	finish()
	got := g.UsageOf(governance.Subject{Team: "team"}, governance.KeyPolicy{}).TeamBudget
	if got.SpentUSDMicros != 12 || got.ReservedUSDMicros != 2000 {
		t.Fatalf("invalid category erased known price or released liability: %+v", got)
	}
	if _, _, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`)); BudgetStatus(err) != 402 {
		t.Fatalf("malformed usage must freeze the blocking scope: %v", err)
	}
}

func localBudgetFixture(cap, window int64) (*governance.Governor, keystore.Principal, router.ChainTarget, *live.State) {
	g := governance.NewGovernor(map[string]governance.TeamPolicy{
		"team": {BudgetMicrosPerMonth: cap, BudgetExceeded: "block"},
	}, limiter.NewMemory(), budget.NewMemory(), nil)
	p := keystore.Principal{Team: "team", KeyID: "key"}
	target := router.ChainTarget{Model: "m", ProviderName: "p", Upstream: "up"}
	table := pricing.New(pricing.OnMissingBlock, map[pricing.Key]pricing.Rate{
		{Provider: "p", Model: "up"}: {InputPerMTok: 1_000_000, OutputPerMTok: 1_000_000},
	})
	state := live.NewState(nil, map[string]config.ModelConfig{"m": {ContextWindow: window}}, table, nil)
	return g, p, target, state
}

func TestLocalBudgetConcurrentAttemptCannotMultiplyOvershoot(t *testing.T) {
	g, p, target, state := localBudgetFixture(100, 1000)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	_, finish, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if _, _, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`)); BudgetStatus(err) != 402 {
		t.Fatalf("second overlapping attempt should refuse exhausted local liability, got %v", err)
	}
}

func TestLocalBudgetIncompleteAttemptRetainsLiability(t *testing.T) {
	for _, window := range []int64{0, 1000} {
		t.Run(map[int64]string{0: "undeclared-window", 1000: "declared-window"}[window], func(t *testing.T) {
			g, p, target, state := localBudgetFixture(100, window)
			req := httptest.NewRequest("POST", "/v1/messages", nil)
			reserved, finish, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`))
			if err != nil {
				t.Fatal(err)
			}
			SettleBudget(reserved, nil, nil, false)
			finish()
			if _, _, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`)); BudgetStatus(err) != 402 {
				t.Fatalf("missing usage must retain liability, got %v", err)
			}
			if got := g.UsageOf(governance.Subject{Team: "team"}, governance.KeyPolicy{}).TeamBudget.SpentUSDMicros; got != 0 {
				t.Fatalf("unknown actual must not be fabricated as spend, got %d", got)
			}
		})
	}
}

func TestLocalBudgetCompleteUsageReleasesUnusedLiability(t *testing.T) {
	g, p, target, state := localBudgetFixture(100, 1000)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	admission := g.PreCheck(governance.Subject{Team: "team"}, governance.KeyPolicy{}, 1).Admission
	reserved, finish, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`), admission)
	if err != nil {
		t.Fatal(err)
	}
	in, out := int64(1), int64(1)
	u := &schema.Usage{InputTokens: &in, OutputTokens: &out}
	g.Settle(governance.Subject{Team: "team"}, governance.KeyPolicy{}, "p", "up", pricing.FromUsage(u), state.Pricing(), admission)
	SettleBudget(reserved, nil, u, true)
	finish()
	if got := g.UsageOf(governance.Subject{Team: "team"}, governance.KeyPolicy{}).TeamBudget.SpentUSDMicros; got != 2 {
		t.Fatalf("observed actual must debit once, got %d", got)
	}
	_, nextFinish, err := ReserveBudget(req, g, p, target, state, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("proved complete inexpensive usage must release excess liability: %v", err)
	}
	nextFinish()
}
