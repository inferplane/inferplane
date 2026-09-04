package config

import "testing"

// server.max_request_bytes (C9): negative rejected, zero defaults to 64 MiB,
// a positive value passes through unchanged — validateBodyLog's posture.
func TestValidateServerMaxRequestBytes(t *testing.T) {
	cases := []struct {
		name    string
		in      int64
		want    int64
		wantErr bool
	}{
		{"negative rejected", -1, 0, true},
		{"zero defaults", 0, 64 << 20, false},
		{"positive passes through", 1 << 20, 1 << 20, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := ServerConfig{MaxRequestBytes: tc.in}
			err := validateServer(&s)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s.MaxRequestBytes != tc.want {
				t.Fatalf("MaxRequestBytes = %d, want %d", s.MaxRequestBytes, tc.want)
			}
		})
	}
}

// models.<name>.context_window: negative rejected by the shared per-model
// validation walk (ValidateModelAliases), zero/positive pass through.
func TestValidateModelContextWindow(t *testing.T) {
	bad := map[string]ModelConfig{"m": {Targets: []Target{{Provider: "p", Model: "u"}}, ContextWindow: -1}}
	if err := ValidateModelAliases(bad); err == nil {
		t.Fatal("negative context_window must be rejected")
	}
	ok := map[string]ModelConfig{"m": {Targets: []Target{{Provider: "p", Model: "u"}}, ContextWindow: 872000}}
	if err := ValidateModelAliases(ok); err != nil {
		t.Fatalf("positive context_window rejected: %v", err)
	}
}
