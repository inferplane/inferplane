package pricing

// Bundled returns the default rate table (µUSD per 1M tokens). Operators
// override via config; self-hosted models supply their own chargeback rates.
func Bundled() map[Key]Rate {
	return map[Key]Rate{
		{"anthropic-direct", "claude-sonnet-4-6"}: {InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000, CacheReadPerMTok: 300_000, CacheWrite5mPerMTok: 3_750_000, CacheWrite1hPerMTok: 6_000_000},
		{"anthropic-direct", "claude-opus-4-8"}:   {InputPerMTok: 5_000_000, OutputPerMTok: 25_000_000, CacheReadPerMTok: 500_000, CacheWrite5mPerMTok: 6_250_000, CacheWrite1hPerMTok: 10_000_000},
		// Opus 5.5 cache read is 0.05x input, not the derived 0.1x.
		{"anthropic-direct", "claude-opus-5-5"}: {InputPerMTok: 4_000_000, OutputPerMTok: 20_000_000, CacheReadPerMTok: 200_000, CacheWrite5mPerMTok: 5_000_000, CacheWrite1hPerMTok: 8_000_000},
	}
}
