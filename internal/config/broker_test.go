package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBrokerConfig writes a one-provider config and returns its path. mode is
// the bedrock provider's auth.mode; cpJSON is the raw control_plane object (or
// "" for no control_plane block at all).
func writeBrokerConfig(t *testing.T, mode, cpJSON string) string {
	t.Helper()
	body := `{"providers":{"br":{"type":"bedrock","region":"us-west-2","auth":{"mode":"` + mode + `"}}}`
	if cpJSON != "" {
		body += `,"control_plane":` + cpJSON
	}
	body += `}`
	f := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestLoadResolvesBrokerTokenRef(t *testing.T) {
	t.Setenv("CP_TOKEN", "heartbeat-secret")
	t.Setenv("CP_BROKER", "broker-secret")
	f := writeBrokerConfig(t, "broker", `{"url":"https://cp.example:7601","token_ref":{"env":"CP_TOKEN"},"broker_token_ref":{"env":"CP_BROKER"}}`)
	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ControlPlane.BrokerToken != "broker-secret" {
		t.Fatalf("BrokerToken = %q, want %q", cfg.ControlPlane.BrokerToken, "broker-secret")
	}
	if cfg.ControlPlane.Token != "heartbeat-secret" {
		t.Fatalf("Token = %q, want %q", cfg.ControlPlane.Token, "heartbeat-secret")
	}
	// The resolved secret must never serialize (json:"-").
	b, err := json.Marshal(cfg.ControlPlane)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "broker-secret") {
		t.Fatalf("serialized control_plane leaks the resolved broker token: %s", b)
	}
}

func TestLoadRejectsUnknownBedrockAuthMode(t *testing.T) {
	// "Broker" is here because the closed set is case-SENSITIVE.
	for _, mode := range []string{"brokre", "Broker", "iam", "sso"} {
		f := writeBrokerConfig(t, mode, "")
		_, err := Load(f)
		if err == nil {
			t.Fatalf("mode %q: Load succeeded, want error", mode)
		}
		for _, want := range []string{"auth.mode", "broker", "pod_identity"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("mode %q: error %q does not contain %q", mode, err, want)
			}
		}
	}
}

func TestLoadAcceptsKnownBedrockAuthModes(t *testing.T) {
	for _, mode := range []string{"", "default", "irsa", "pod_identity", "profile", "static"} {
		f := writeBrokerConfig(t, mode, "")
		if _, err := Load(f); err != nil {
			t.Fatalf("mode %q: Load: %v", mode, err)
		}
	}
}

func TestLoadRejectsBrokerModeWithoutControlPlane(t *testing.T) {
	f := writeBrokerConfig(t, "broker", "")
	_, err := Load(f)
	if err == nil {
		t.Fatal("Load succeeded, want error")
	}
	if !strings.Contains(err.Error(), "control_plane") {
		t.Fatalf("error %q does not contain %q", err, "control_plane")
	}
}

func TestLoadRejectsBrokerModeWithoutBrokerTokenRef(t *testing.T) {
	t.Setenv("CP_TOKEN", "x")
	f := writeBrokerConfig(t, "broker", `{"url":"https://cp.example:7601","token_ref":{"env":"CP_TOKEN"}}`)
	_, err := Load(f)
	if err == nil {
		t.Fatal("Load succeeded, want error")
	}
	if !strings.Contains(err.Error(), "broker_token_ref") {
		t.Fatalf("error %q does not contain %q", err, "broker_token_ref")
	}
}

func TestBrokerModeURLSchemeRules(t *testing.T) {
	t.Setenv("CP_BROKER", "b")
	t.Setenv("CP_TOKEN", "h")
	for _, tc := range []struct {
		url     string
		wantErr bool
	}{
		{"https://cp.example:7601", false},
		{"http://127.0.0.1:7601", false},
		{"http://localhost:7601", false},
		{"http://[::1]:7601", false},
		{"http://cp.example:7601", true},
		{"http://10.0.0.5:7601", true},
	} {
		f := writeBrokerConfig(t, "broker", `{"url":"`+tc.url+`","token_ref":{"env":"CP_TOKEN"},"broker_token_ref":{"env":"CP_BROKER"}}`)
		_, err := Load(f)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("url %q: Load succeeded, want error", tc.url)
			}
			if !strings.Contains(err.Error(), "plaintext") {
				t.Fatalf("url %q: error %q does not contain %q", tc.url, err, "plaintext")
			}
		} else if err != nil {
			t.Fatalf("url %q: Load: %v", tc.url, err)
		}
	}
}

func TestLoadRejectsBrokerTokenEqualToHeartbeatToken(t *testing.T) {
	t.Setenv("CP_TOKEN", "same-secret")
	t.Setenv("CP_BROKER", "same-secret")
	f := writeBrokerConfig(t, "broker", `{"url":"https://cp.example:7601","token_ref":{"env":"CP_TOKEN"},"broker_token_ref":{"env":"CP_BROKER"}}`)
	_, err := Load(f)
	if err == nil {
		t.Fatal("Load succeeded, want error")
	}
	for _, want := range []string{"broker_token_ref", "token_ref"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
	// Flip the broker token to a distinct value: the same config must now
	// load — proving the rejection is the equality, not the presence of
	// both refs.
	t.Setenv("CP_BROKER", "different-secret")
	if _, err := Load(f); err != nil {
		t.Fatalf("Load with distinct tokens: %v", err)
	}
}

func TestNonBedrockProviderAuthModeStillIgnored(t *testing.T) {
	// auth.mode is only wired into Settings for bedrock (internal/live), so
	// ADR-040 deliberately does not widen the closed set to types where the
	// field has never had any effect.
	t.Setenv("AK", "k")
	f := filepath.Join(t.TempDir(), "c.json")
	body := `{"providers":{"a":{"type":"anthropic","api_key_ref":{"env":"AK"},"auth":{"mode":"whatever"}}}}`
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f); err != nil {
		t.Fatalf("Load: %v", err)
	}
}
