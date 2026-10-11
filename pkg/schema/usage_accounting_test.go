package schema

import (
	"encoding/json"
	"testing"
)

func TestCacheWritePartialSplitPreservesTotal(t *testing.T) {
	for _, tc := range []struct {
		name, raw  string
		five, hour int64
	}{
		{"missing hour", `{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":40}}`, 100, 0},
		{"missing five", `{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_1h_input_tokens":40}}`, 60, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var u Usage
			if err := json.Unmarshal([]byte(tc.raw), &u); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(u)
			five, hour := u.CacheWriteTiers()
			if five != tc.five || hour != tc.hour {
				t.Fatalf("got %d+%d, want %d+%d; known total must survive", five, hour, tc.five, tc.hour)
			}
			after, _ := json.Marshal(u)
			if string(before) != string(after) {
				t.Fatal("accounting changed wire bytes")
			}
		})
	}
}
