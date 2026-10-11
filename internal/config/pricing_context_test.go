package config

import (
	"fmt"
	"testing"
)

func TestContextPricingConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name, rate string
		valid      bool
	}{
		{"valid", `"input_per_mtok":2,"output_per_mtok":10,"short_context_limit":272000,"long_context":{"input_per_mtok":4,"output_per_mtok":15}`, true},
		{"missing_long", `"input_per_mtok":2,"short_context_limit":272000`, false},
		{"missing_limit", `"input_per_mtok":2,"long_context":{"input_per_mtok":4}`, false},
		{"negative_limit", `"input_per_mtok":2,"short_context_limit":-1,"long_context":{"input_per_mtok":4}`, false},
		{"unpriced_long", `"input_per_mtok":2,"short_context_limit":272000,"long_context":{}`, false},
		{"negative_long", `"input_per_mtok":2,"short_context_limit":272000,"long_context":{"input_per_mtok":-4}`, false},
		{"nested_tier", `"input_per_mtok":2,"short_context_limit":272000,"long_context":{"input_per_mtok":4,"short_context_limit":1,"long_context":{"input_per_mtok":8}}`, false},
		{"negative_base", `"input_per_mtok":-2,"output_per_mtok":10`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadRaw(writeConfig(t, fmt.Sprintf(`{"pricing":{"overrides":{"p":{"m":{%s}}}}}`, tc.rate)))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
