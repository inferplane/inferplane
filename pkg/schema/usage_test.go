package schema

import "testing"

func i64(v int64) *int64 { return &v }

func TestMergeUsage_nilCases(t *testing.T) {
	if got := MergeUsage(nil, nil); got != nil {
		t.Fatalf("nil+nil = %+v, want nil", got)
	}
	acc := &Usage{InputTokens: i64(10)}
	if got := MergeUsage(acc, nil); got != acc {
		t.Fatal("nil next must leave acc untouched")
	}
	next := &Usage{OutputTokens: i64(5)}
	got := MergeUsage(nil, next)
	if got == next {
		t.Fatal("nil acc must return a COPY, not the provider's frame (callers keep folding into the result)")
	}
	if got.OutputTokens == nil || *got.OutputTokens != 5 {
		t.Fatalf("got %+v", got)
	}
}

// TestMergeUsage_messageStartThenDelta is the exact production shape the
// pre-ADR-030 code got wrong: message_start carries input + cache counts,
// message_delta carries output only. Folding must keep all of them.
func TestMergeUsage_messageStartThenDelta(t *testing.T) {
	messageStart := &Usage{
		InputTokens:              i64(1000),
		CacheReadInputTokens:     i64(4096),
		CacheCreationInputTokens: i64(512),
	}
	messageDelta := &Usage{OutputTokens: i64(77)}

	got := MergeUsage(MergeUsage(nil, messageStart), messageDelta)

	if got.InputTokens == nil || *got.InputTokens != 1000 {
		t.Errorf("input_tokens lost: %+v", got.InputTokens)
	}
	if got.OutputTokens == nil || *got.OutputTokens != 77 {
		t.Errorf("output_tokens: %+v", got.OutputTokens)
	}
	if got.CacheReadInputTokens == nil || *got.CacheReadInputTokens != 4096 {
		t.Errorf("cache_read lost: %+v", got.CacheReadInputTokens)
	}
	if got.CacheCreationInputTokens == nil || *got.CacheCreationInputTokens != 512 {
		t.Errorf("cache_creation lost: %+v", got.CacheCreationInputTokens)
	}
}

// TestMergeUsage_foldsNotSums: a count repeated across frames must not double.
func TestMergeUsage_foldsNotSums(t *testing.T) {
	first := &Usage{InputTokens: i64(100), OutputTokens: i64(10)}
	second := &Usage{InputTokens: i64(100), OutputTokens: i64(25)}
	got := MergeUsage(MergeUsage(nil, first), second)
	if *got.InputTokens != 100 {
		t.Errorf("input summed instead of folded: %d, want 100", *got.InputTokens)
	}
	if *got.OutputTokens != 25 {
		t.Errorf("output = %d, want the latest value 25", *got.OutputTokens)
	}
}

// TestMergeUsage_explicitZeroPreserved: an explicit 0 is a real observation
// (pkg/schema/response.go's whole reason for *int64) and must not be treated
// as "absent" and skipped by the fold.
func TestMergeUsage_explicitZeroPreserved(t *testing.T) {
	acc := &Usage{CacheReadInputTokens: i64(4096)}
	next := &Usage{CacheReadInputTokens: i64(0)}
	got := MergeUsage(acc, next)
	if got.CacheReadInputTokens == nil || *got.CacheReadInputTokens != 0 {
		t.Fatalf("explicit 0 must overwrite 4096, got %+v", got.CacheReadInputTokens)
	}
}

func TestMergeUsage_doesNotMutateInputs(t *testing.T) {
	acc := &Usage{InputTokens: i64(10)}
	next := &Usage{OutputTokens: i64(5)}
	_ = MergeUsage(acc, next)
	if acc.OutputTokens != nil {
		t.Fatal("acc was mutated")
	}
	if next.InputTokens != nil {
		t.Fatal("next was mutated")
	}
}

func TestMergeUsage_cacheCreationTiers(t *testing.T) {
	// 5m arrives first, 1h in a later frame — both must survive, separately,
	// because they are priced at different multiples of the input rate.
	acc := MergeUsage(nil, &Usage{CacheCreation: &CacheCreation{Ephemeral5mInputTokens: i64(300)}})
	got := MergeUsage(acc, &Usage{CacheCreation: &CacheCreation{Ephemeral1hInputTokens: i64(700)}})

	if got.CacheCreation == nil {
		t.Fatal("cache_creation lost")
	}
	if got.CacheCreation.Ephemeral5mInputTokens == nil || *got.CacheCreation.Ephemeral5mInputTokens != 300 {
		t.Errorf("5m tier: %+v", got.CacheCreation.Ephemeral5mInputTokens)
	}
	if got.CacheCreation.Ephemeral1hInputTokens == nil || *got.CacheCreation.Ephemeral1hInputTokens != 700 {
		t.Errorf("1h tier: %+v", got.CacheCreation.Ephemeral1hInputTokens)
	}
}

func TestCacheWriteTiers(t *testing.T) {
	cases := []struct {
		name   string
		u      *Usage
		want5m int64
		want1h int64
	}{
		{"nil usage", nil, 0, 0},
		{"nothing set", &Usage{}, 0, 0},
		{
			"flat total only falls back to the cheaper 5m tier",
			&Usage{CacheCreationInputTokens: i64(500)},
			500, 0,
		},
		{
			"TTL split is authoritative",
			&Usage{CacheCreation: &CacheCreation{Ephemeral5mInputTokens: i64(300), Ephemeral1hInputTokens: i64(700)}},
			300, 700,
		},
		{
			"1h only",
			&Usage{CacheCreation: &CacheCreation{Ephemeral1hInputTokens: i64(700)}},
			0, 700,
		},
		{
			// The double-count guard: a provider sending BOTH must be billed
			// the split, never split+flat.
			"split wins over flat, never summed",
			&Usage{
				CacheCreationInputTokens: i64(1000),
				CacheCreation:            &CacheCreation{Ephemeral5mInputTokens: i64(300), Ephemeral1hInputTokens: i64(700)},
			},
			300, 700,
		},
		{
			// An all-zero split must not mask a non-zero flat total.
			"all-zero split falls through to flat",
			&Usage{
				CacheCreationInputTokens: i64(1000),
				CacheCreation:            &CacheCreation{Ephemeral5mInputTokens: i64(0), Ephemeral1hInputTokens: i64(0)},
			},
			1000, 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got5m, got1h := c.u.CacheWriteTiers()
			if got5m != c.want5m || got1h != c.want1h {
				t.Fatalf("got (5m=%d, 1h=%d), want (5m=%d, 1h=%d)", got5m, got1h, c.want5m, c.want1h)
			}
		})
	}
}

func TestMergeUsage_cacheCreationNilAccCopies(t *testing.T) {
	next := &Usage{CacheCreation: &CacheCreation{Ephemeral5mInputTokens: i64(5)}}
	got := MergeUsage(nil, next)
	if got.CacheCreation == next.CacheCreation {
		t.Fatal("nested CacheCreation must be copied, not aliased to the provider's frame")
	}
	if *got.CacheCreation.Ephemeral5mInputTokens != 5 {
		t.Fatalf("got %+v", got.CacheCreation)
	}
}
