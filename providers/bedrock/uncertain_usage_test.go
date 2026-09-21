package bedrock

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

func TestSDKMissingUsageAndUnknownTTLRemainUncertain(t *testing.T) {
	for _, u := range []*brtypes.TokenUsage{
		nil, {},
		{InputTokens: aws.Int32(1), OutputTokens: aws.Int32(2),
			CacheDetails: []brtypes.CacheDetail{{Ttl: "future-ttl", InputTokens: aws.Int32(10)}}},
		{InputTokens: aws.Int32(1), OutputTokens: aws.Int32(2), CacheWriteInputTokens: aws.Int32(100),
			CacheDetails: []brtypes.CacheDetail{{Ttl: brtypes.CacheTTLFiveMinutes, InputTokens: aws.Int32(40)},
				{Ttl: brtypes.CacheTTLOneHour, InputTokens: aws.Int32(20)}}},
	} {
		if !uncertainSDKUsage(u) {
			t.Fatal("incomplete SDK usage became precise")
		}
	}
	if uncertainSDKUsage(&brtypes.TokenUsage{InputTokens: aws.Int32(0), OutputTokens: aws.Int32(0)}) {
		t.Fatal("explicit zero usage is known")
	}
}

func TestConverseAdaptersPreserveAccountingUncertainty(t *testing.T) {
	fc := &fakeConverser{resp: ConverseResponse{UsageUncertain: true}}
	p := &provider{conv: fc, modelAPI: map[string]string{"m": "converse"}}
	req := &providers.ProxyRequest{Model: "m", Upstream: "m", RawBody: []byte(`{"messages":[]}`)}
	resp, err := p.Complete(context.Background(), req)
	if err != nil || resp.Parsed == nil || !resp.Parsed.Usage.AccountingUncertain {
		t.Fatalf("missing complete usage converted to exact zero: %v", err)
	}
	for _, events := range [][]ConverseStreamEvent{
		{{Kind: eventMessageStop, StopReason: "end_turn"}},
		{{Kind: eventMessageStop, StopReason: "end_turn"}, {Kind: eventUsage, UsageUncertain: true}},
	} {
		fc.streamEv = events
		seq, err := p.Stream(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		saw := false
		for ev, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Chunk != nil && ev.Chunk.Usage != nil {
				saw = true
				if !ev.Chunk.Usage.AccountingUncertain {
					t.Fatal("stream adapter invented exact zero usage")
				}
			}
		}
		if !saw {
			t.Fatal("missing terminal observation")
		}
	}
}

func TestSDKFlatCacheWritesRequireTTLBreakdown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		total     *int32
		details   []brtypes.CacheDetail
		uncertain bool
	}{
		{name: "positive flat total", total: aws.Int32(10), uncertain: true},
		{name: "negative flat total", total: aws.Int32(-1), uncertain: true},
		{name: "explicit zero", total: aws.Int32(0)},
		{name: "no cache writes"},
		{name: "known split", total: aws.Int32(10), details: []brtypes.CacheDetail{
			{Ttl: brtypes.CacheTTLFiveMinutes, InputTokens: aws.Int32(4)},
			{Ttl: brtypes.CacheTTLOneHour, InputTokens: aws.Int32(6)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &brtypes.TokenUsage{InputTokens: aws.Int32(1), OutputTokens: aws.Int32(2), CacheWriteInputTokens: tc.total, CacheDetails: tc.details}
			if got := uncertainSDKUsage(u); got != tc.uncertain {
				t.Fatalf("uncertainSDKUsage = %v, want %v", got, tc.uncertain)
			}
		})
	}
}

func TestConverseFlatCacheWritesPreserveEstimateAndUncertainty(t *testing.T) {
	sdkUsage := &brtypes.TokenUsage{
		InputTokens: aws.Int32(7), OutputTokens: aws.Int32(3), CacheWriteInputTokens: aws.Int32(100),
	}
	uncertain := uncertainSDKUsage(sdkUsage)
	fc := &fakeConverser{
		resp: ConverseResponse{InputTokens: 7, OutputTokens: 3, CacheWriteTotal: 100, UsageUncertain: uncertain},
		streamEv: []ConverseStreamEvent{
			{Kind: eventMessageStop, StopReason: "end_turn"},
			{Kind: eventUsage, InputTokens: 7, OutputTokens: 3, CacheWriteTotal: 100, UsageUncertain: uncertain},
		},
	}
	p := &provider{conv: fc, modelAPI: map[string]string{"m": "converse"}}
	req := &providers.ProxyRequest{Model: "m", Upstream: "m", RawBody: []byte(`{"messages":[]}`)}
	check := func(u *schema.Usage) {
		t.Helper()
		if u == nil || !u.AccountingUncertain {
			t.Fatal("flat cache usage became exact")
		}
		w5, w1 := u.CacheWriteTiers()
		if w5 != 100 || w1 != 0 {
			t.Fatalf("cheaper-tier estimate changed: 5m=%d 1h=%d", w5, w1)
		}
		wire, err := json.Marshal(u)
		if err != nil || strings.Contains(string(wire), "Uncertain") || strings.Contains(string(wire), "uncertain") {
			t.Fatalf("observation-only uncertainty entered wire: %s %v", wire, err)
		}
	}
	resp, err := p.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	check(resp.Parsed.Usage)
	seq, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	sawUsage := false
	for ev, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Chunk != nil && ev.Chunk.Usage != nil {
			sawUsage = true
			check(ev.Chunk.Usage)
		}
	}
	if !sawUsage {
		t.Fatal("missing terminal usage")
	}
}
