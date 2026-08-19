package live

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inferplane/inferplane/internal/config"
)

// fakeCredSource implements providers.CredentialSource without a network. The
// field type in live.Deps is the interface, so this file needs no providers
// import (the package's import-guard test only inspects non-test files, but
// keeping the test's imports narrow matches the spirit of it).
type fakeCredSource struct {
	err   error
	calls int
}

func (f *fakeCredSource) Credentials(context.Context) (string, string, string, time.Time, error) {
	f.calls++
	if f.err != nil {
		return "", "", "", time.Time{}, f.err
	}
	return "ASIAFAKE", "shhh", "tok", time.Now().Add(time.Hour), nil
}

// brokerConfig is a minimal topology whose single provider opted into
// credential brokering (ADR-040). The control_plane block is absent on purpose:
// internal/config validates the broker preconditions at LOAD time, and this
// test drives the topology builder directly.
func brokerConfig() *config.Config {
	pc := config.ProviderConfig{Type: "bedrock", Region: "us-west-2"}
	pc.Auth.Mode = "broker"
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"bedrock-broker": pc},
		Models: map[string]config.ModelConfig{
			"claude": {Targets: []config.Target{{Provider: "bedrock-broker", Model: "anthropic.claude-sonnet-4-6-v1:0"}}},
		},
	}
	cfg.Pricing.OnMissing = "allow"
	return cfg
}

// TestBuildStateWithThreadsCredentialSourceToBedrock is the Phase 5 wiring
// proof. It asserts the injected source actually reaches provider construction,
// end to end, by using the provider's own fail-closed behavior as the detector:
// broker mode performs an eager Retrieve during construction (ADR-040 invariant
// #4), so the source's call counter can only advance if BuildStateWith really
// passed it into providers.Config.
func TestBuildStateWithThreadsCredentialSourceToBedrock(t *testing.T) {
	src := &fakeCredSource{}
	st, _, err := BuildStateWith(brokerConfig(), Deps{Credentials: src})
	if err != nil {
		t.Fatalf("BuildStateWith with a working credential source: %v", err)
	}
	if st == nil {
		t.Fatal("BuildStateWith returned a nil State")
	}
	if src.calls != 1 {
		t.Fatalf("credential source calls = %d, want 1 — the source did not reach the bedrock provider's eager fetch", src.calls)
	}
}

// TestBuildStateWithoutCredentialSourceFailsBrokerProvider is the other half of
// the same wiring proof, and the one that matters for security: with nothing
// injected, a broker-mode provider must fail the whole build. It must NOT
// build successfully and quietly sign with the node's own AWS identity
// (ADR-040 fail-closed invariant #1). This is also what makes a UI write that
// newly sets auth.mode "broker" a 400 rather than a silent downgrade.
func TestBuildStateWithoutCredentialSourceFailsBrokerProvider(t *testing.T) {
	if _, _, err := BuildStateWith(brokerConfig(), Deps{}); err == nil {
		t.Fatal("BuildStateWith(Deps{}) built a broker-mode provider; want a construction error")
	} else if !strings.Contains(err.Error(), "default AWS credential chain") {
		t.Fatalf("error %q does not name the refused fall-back to the default AWS credential chain", err)
	}
	// The compatibility wrapper must behave identically — it is what every
	// pre-ADR-040 caller and test still uses.
	if _, _, err := BuildState(brokerConfig()); err == nil {
		t.Fatal("BuildState built a broker-mode provider with no injected source; want a construction error")
	}
}

// TestBuildStateWithPropagatesCredentialFetchFailure pins ADR-040 invariant #4
// at the BuildState boundary: a broker that cannot be reached fails the
// topology build (boot fails; a reload keeps the old generation live, ADR-006
// rollback) instead of publishing a generation that errors on user traffic.
func TestBuildStateWithPropagatesCredentialFetchFailure(t *testing.T) {
	src := &fakeCredSource{err: errors.New("broker unreachable")}
	_, _, err := BuildStateWith(brokerConfig(), Deps{Credentials: src})
	if err == nil {
		t.Fatal("BuildStateWith with a failing credential source succeeded; want the build to fail")
	}
	if !strings.Contains(err.Error(), "broker unreachable") {
		t.Fatalf("error %q does not carry the source's failure", err)
	}
	if src.calls != 1 {
		t.Fatalf("credential source calls = %d, want 1 (eager fetch during construction)", src.calls)
	}
}

// TestBuildStateWithLeavesNonBrokerProvidersUnchanged: injecting a source must
// not alter any provider that did not ask for one, so the default local-IAM
// posture stays byte-identical (ADR-040 consequences).
func TestBuildStateWithLeavesNonBrokerProvidersUnchanged(t *testing.T) {
	src := &fakeCredSource{}
	if _, _, err := BuildStateWith(sampleConfig(), Deps{Credentials: src}); err != nil {
		t.Fatalf("BuildStateWith on a non-broker topology: %v", err)
	}
	if src.calls != 0 {
		t.Fatalf("credential source calls = %d, want 0 — a non-broker provider must never fetch", src.calls)
	}
}
