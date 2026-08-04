package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validBatch() *UsageBatch {
	return &UsageBatch{
		Dataplane:   "dp-1",
		WindowStart: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
		WindowEnd:   time.Date(2026, 8, 4, 12, 1, 0, 0, time.UTC),
		Entries: []UsageEntry{
			{
				Team: "demo", User: "intern-01", Model: "claude-opus-5",
				SpentMicroUSD: 1234, InputTokens: 500, OutputTokens: 120,
				CacheReadTokens: 300, CacheWrite5mTokens: 50, CacheWrite1hTokens: 0,
			},
		},
	}
}

func TestValidateOK(t *testing.T) {
	if err := validBatch().Validate(); err != nil {
		t.Fatalf("valid batch rejected: %v", err)
	}
}

// A user-less entry is valid: user attribution is optional (key without owner).
func TestValidateEmptyUserOK(t *testing.T) {
	b := validBatch()
	b.Entries[0].User = ""
	if err := b.Validate(); err != nil {
		t.Fatalf("empty user rejected: %v", err)
	}
}

func TestValidateRejections(t *testing.T) {
	cases := map[string]struct {
		mutate  func(*UsageBatch)
		wantSub string // distinct error substrings — each violation identifiable
	}{
		"empty dataplane": {func(b *UsageBatch) { b.Dataplane = "" }, "dataplane"},
		"window inverted": {func(b *UsageBatch) { b.WindowEnd = b.WindowStart.Add(-time.Minute) }, "window"},
		"window equal":    {func(b *UsageBatch) { b.WindowEnd = b.WindowStart }, "window"},
		"zero start": {func(b *UsageBatch) {
			b.WindowStart = time.Time{} // absent/null on the wire decodes to zero — must not pass
		}, "window"},
		"zero end":        {func(b *UsageBatch) { b.WindowEnd = time.Time{} }, "window"},
		"negative spend":  {func(b *UsageBatch) { b.Entries[0].SpentMicroUSD = -1 }, "negative"},
		"negative tokens": {func(b *UsageBatch) { b.Entries[0].CacheWrite1hTokens = -5 }, "negative"},
		"missing team":    {func(b *UsageBatch) { b.Entries[0].Team = "" }, "team"},
		"missing model":   {func(b *UsageBatch) { b.Entries[0].Model = "" }, "model"},
		"duplicate key": {func(b *UsageBatch) {
			b.Entries = append(b.Entries, b.Entries[0]) // same (team,user,model) twice → PG PK poison
		}, "duplicate"},
		"NUL in team": {func(b *UsageBatch) {
			b.Entries[0].Team = "de\x00mo" // valid JSON, invalid Postgres TEXT → eternal 503
		}, "NUL"},
		"NUL in user":  {func(b *UsageBatch) { b.Entries[0].User = "u\x00" }, "NUL"},
		"NUL in model": {func(b *UsageBatch) { b.Entries[0].Model = "m\x00" }, "NUL"},
		"NUL in dataplane": {func(b *UsageBatch) {
			b.Dataplane = "dp\x000"
		}, "NUL"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			b := validBatch()
			tc.mutate(b)
			err := b.Validate()
			if err == nil {
				t.Fatalf("%s: accepted", name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("%s: error %q does not identify the violation (want substring %q)", name, err, tc.wantSub)
			}
		})
	}
}

// Wire shape is the spec-D3 contract — JSON tags pinned so a rename breaks loudly.
func TestJSONWireShape(t *testing.T) {
	raw, err := json.Marshal(validBatch())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"dataplane"`, `"window_start"`, `"window_end"`, `"entries"`,
		`"team"`, `"user"`, `"model"`, `"spent_micro_usd"`,
		`"input_tokens"`, `"output_tokens"`, `"cache_read_tokens"`,
		`"cache_write_5m_tokens"`, `"cache_write_1h_tokens"`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("wire JSON missing %s: %s", key, raw)
		}
	}
	// Value-per-key assertions: a tag swap between token fields (e.g. 5m/1h)
	// would survive a self-round-trip — pin each encoded value instead.
	var m struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	e := m.Entries[0]
	for key, want := range map[string]float64{
		"spent_micro_usd": 1234, "input_tokens": 500, "output_tokens": 120,
		"cache_read_tokens": 300, "cache_write_5m_tokens": 50, "cache_write_1h_tokens": 0,
	} {
		if got, ok := e[key].(float64); !ok || got != want {
			t.Fatalf("wire key %q = %v, want %v", key, e[key], want)
		}
	}
	var back UsageBatch
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Entries[0].SpentMicroUSD != 1234 {
		t.Fatalf("round trip lost spend: %+v", back.Entries[0])
	}
}

func TestValidateSaneBounds(t *testing.T) {
	b := validBatch()
	b.Entries[0].InputTokens = int64(1e15) + 1
	if err := b.Validate(); err == nil {
		t.Fatal("overflow-scale count accepted")
	}
	b2 := validBatch()
	b2.WindowStart = time.Date(2263, 1, 1, 0, 0, 0, 0, time.UTC)
	b2.WindowEnd = b2.WindowStart.Add(time.Minute)
	if err := b2.Validate(); err == nil {
		t.Fatal("out-of-range window accepted (UnixNano collision risk)")
	}
}
