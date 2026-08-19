// Tests for the ADR-040 credential-broker boot validation and the "unset ⇒
// route absent" opt-in criterion. The broker handler itself is covered by
// internal/controlplane/broker_test.go against a fake STS; nothing here may
// exercise buildMux WITH the role ARN set — that path constructs a real
// sts.Client via LoadDefaultConfig, which is environment-dependent.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateBrokerEnv(t *testing.T) {
	const arn = "arn:aws:iam::123456789012:role/r"
	tests := []struct {
		name        string
		token       string
		brokerToken string
		roleARN     string
		wantErr     string // empty ⇒ no error
	}{
		{"arn unset is a no-op", "t", "", "", ""},
		{"broker token without arn is only a warning", "t", "b", "", ""},
		{"arn set but broker token missing", "t", "", arn, "INFERPLANED_BROKER_TOKEN must be set"},
		{"jwt-shaped broker token", "t", "a.b.c", arn, "JWT-shaped"},
		{"broker token equals heartbeat token", "same", "same", arn, "must differ from INFERPLANED_TOKEN"},
		{"valid distinct pair", "t", "b", arn, ""},
		// An unauthenticated loopback inferplaned with a broker token set is
		// legitimate: the broker route always requires ITS token.
		{"empty heartbeat token with broker token", "", "b", arn, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBrokerEnv(tc.token, tc.brokerToken, tc.roleARN)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateBrokerEnv(%q, %q, %q) = %v, want nil", tc.token, tc.brokerToken, tc.roleARN, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateBrokerEnv(%q, %q, %q) = %v, want error containing %q", tc.token, tc.brokerToken, tc.roleARN, err, tc.wantErr)
			}
		})
	}
}

// TestBuildMuxWithoutBrokerARNHasNoCredentialsRoute is ADR-040's "unset ⇒
// route absent ⇒ default behavior byte-identical" criterion: with no role ARN
// the credentials route is not registered at all, so the mux answers 404.
func TestBuildMuxWithoutBrokerARNHasNoCredentialsRoute(t *testing.T) {
	t.Setenv("INFERPLANED_BROKER_ROLE_ARN", "")
	t.Setenv("INFERPLANED_BROKER_TOKEN", "")
	mux, _, closePG, err := buildMux("", "", nil)
	if closePG != nil {
		defer closePG()
	}
	if err != nil {
		t.Fatalf("buildMux: %v", err)
	}
	req := httptest.NewRequest("POST", "/v1alpha1/credentials", strings.NewReader(`{"dataplane":"d"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /v1alpha1/credentials with broker unset: status = %d, want 404", rec.Code)
	}
}
