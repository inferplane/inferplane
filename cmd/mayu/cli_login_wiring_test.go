package main

import (
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/config"
)

// These are narrow wiring tests for the three small ADR-028 glue functions in
// gateway.go — the substantive logic (config validation, mount gating, mint
// behavior) is already covered in internal/config and internal/server/authapi;
// this just pins that gateway.go reads the RIGHT fields off *config.Config.

func TestCliVerifierNilWhenCLILoginAbsent(t *testing.T) {
	cfg := &config.Config{}
	if cliVerifier(cfg) != nil {
		t.Fatal("nil OIDC block: cliVerifier must be nil")
	}
	cfg.Server.AdminAuth.OIDC = &config.OIDCConfig{Issuer: "https://idp.example.com", ClientID: "console-client"}
	if cliVerifier(cfg) != nil {
		t.Fatal("cli_login absent: cliVerifier must be nil")
	}
	cfg.Server.AdminAuth.OIDC.CLILogin = &config.CLILoginConfig{Enabled: false, ClientID: "cli-client"}
	if cliVerifier(cfg) != nil {
		t.Fatal("cli_login.enabled=false: cliVerifier must be nil")
	}
}

func TestCliVerifierNonNilWhenEnabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.AdminAuth.OIDC = &config.OIDCConfig{
		Issuer:   "https://idp.example.com",
		ClientID: "console-client",
		CLILogin: &config.CLILoginConfig{Enabled: true, ClientID: "cli-client"},
	}
	if cliVerifier(cfg) == nil {
		t.Fatal("cli_login.enabled=true: cliVerifier must be non-nil")
	}
}

func TestCliAuthConfigView(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.AdminAuth.OIDC = &config.OIDCConfig{
		Issuer:   "https://idp.example.com",
		ClientID: "console-client",
		CLILogin: &config.CLILoginConfig{Enabled: true, ClientID: "cli-client"},
	}
	view := cliAuthConfigView(cfg)()
	if !view.CLI || view.Issuer != "https://idp.example.com" || view.ClientID != "cli-client" {
		t.Fatalf("cliAuthConfigView: %+v", view)
	}
}

func TestCliAuthConfigViewDisabledIsSafeToCall(t *testing.T) {
	// server.DataMux never invokes this closure when cliVerifier is nil, but
	// the closure itself must not panic if called directly (defense-in-depth,
	// same posture as authConfigView's own nil check).
	view := cliAuthConfigView(&config.Config{})()
	if view.CLI {
		t.Fatalf("disabled cli_login: view.CLI must be false, got %+v", view)
	}
}

func TestCliKeyTTLDefaultsAndReadsConfig(t *testing.T) {
	if got := cliKeyTTL(&config.Config{}); got != 8*time.Hour {
		t.Fatalf("nil OIDC: cliKeyTTL = %s, want 8h default", got)
	}
	cfg := &config.Config{}
	cfg.Server.AdminAuth.OIDC = &config.OIDCConfig{
		CLILogin: &config.CLILoginConfig{Enabled: true, ClientID: "cli-client", KeyTTL: "2h"},
	}
	if got := cliKeyTTL(cfg); got != 2*time.Hour {
		t.Fatalf("cliKeyTTL = %s, want 2h", got)
	}
}
