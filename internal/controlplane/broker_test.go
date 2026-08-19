// Tests for the ADR-040 credential broker: fake STS only — no AWS SDK call
// leaves the process, no credentials, no network. Every request is driven
// through a real http.ServeMux (mounted with Mount) so the route pattern and
// the auth wrapper are both exercised.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
)

type fakeSTS struct {
	calls []*sts.AssumeRoleInput
	err   error   // returned on EVERY call
	errs  []error // per-call errors (index = call number); overrides err when non-nil
	out   *sts.AssumeRoleOutput
}

// AssumeRole records a COPY of the input, never the pointer: production
// assumeRole MUTATES in.SourceIdentity between the first attempt and the
// retry, so storing the pointer would make both recorded calls show the
// mutated (nil) value and TestBrokerRetriesWithoutSourceIdentity would pass
// for the wrong reason. The struct copy captures RoleArn, RoleSessionName,
// SourceIdentity (nil-ness included), DurationSeconds, and the tags as they
// were AT CALL TIME.
func (f *fakeSTS) AssumeRole(_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	cp := *in
	f.calls = append(f.calls, &cp)
	n := len(f.calls) - 1
	if n < len(f.errs) && f.errs[n] != nil {
		return nil, f.errs[n]
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func okCreds(exp time.Time) *sts.AssumeRoleOutput {
	return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
		AccessKeyId:     aws.String("ASIAFAKE"),
		SecretAccessKey: aws.String("fake-secret"),
		SessionToken:    aws.String("fake-session"),
		Expiration:      aws.Time(exp),
	}}
}

const (
	brokerTestToken = "broker-tok"
	brokerTestARN   = "arn:aws:iam::123456789012:role/inferplane-broker"
)

func brokerMux(f *fakeSTS) *http.ServeMux {
	mux := http.NewServeMux()
	NewBrokerServer(brokerTestToken, brokerTestARN, f).Mount(mux)
	return mux
}

// postCredentials sends POST /v1alpha1/credentials with the raw Authorization
// header value (empty ⇒ no header at all).
func postCredentials(mux *http.ServeMux, authorization, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1alpha1/credentials", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestBrokerHappyPath(t *testing.T) {
	exp := time.Date(2026, 8, 18, 12, 34, 56, 0, time.UTC)
	f := &fakeSTS{out: okCreds(exp)}
	mux := brokerMux(f)

	rec := postCredentials(mux, "Bearer "+brokerTestToken, `{"dataplane":"host-01JABC","provider":"bedrock"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
	var got struct {
		AccessKeyID     string    `json:"accessKeyId"`
		SecretAccessKey string    `json:"secretAccessKey"`
		SessionToken    string    `json:"sessionToken"`
		Expiration      time.Time `json:"expiration"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.AccessKeyID != "ASIAFAKE" || got.SecretAccessKey != "fake-secret" || got.SessionToken != "fake-session" {
		t.Errorf("credential fields = %+v, want the fake's values", got)
	}
	if !got.Expiration.Equal(exp) {
		t.Errorf("expiration = %v, want %v", got.Expiration, exp)
	}
	if len(f.calls) != 1 {
		t.Fatalf("AssumeRole calls = %d, want 1", len(f.calls))
	}
	in := f.calls[0]
	if in.RoleSessionName == nil || *in.RoleSessionName != "host-01JABC" {
		t.Errorf("RoleSessionName = %v, want host-01JABC", in.RoleSessionName)
	}
	if in.SourceIdentity == nil || *in.SourceIdentity != "host-01JABC" {
		t.Errorf("SourceIdentity = %v, want host-01JABC", in.SourceIdentity)
	}
	if in.DurationSeconds == nil || *in.DurationSeconds != 3600 {
		t.Errorf("DurationSeconds = %v, want 3600", in.DurationSeconds)
	}
	if in.RoleArn == nil || *in.RoleArn != brokerTestARN {
		t.Errorf("RoleArn = %v, want %q", in.RoleArn, brokerTestARN)
	}
	if len(in.Tags) != 1 || *in.Tags[0].Key != "dataplane" || *in.Tags[0].Value != "host-01JABC" {
		t.Errorf("Tags = %+v, want one dataplane=host-01JABC tag", in.Tags)
	}
}

func TestBrokerRejectsMissingAndWrongBearer(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
	}{
		{"no Authorization header", ""},
		{"empty bearer token", "Bearer "},
		{"wrong token", "Bearer wrong-token"},
		{"right token with a suffix", "Bearer " + brokerTestToken + "-with-suffix"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSTS{out: okCreds(time.Now().Add(time.Hour))}
			rec := postCredentials(brokerMux(f), tc.authorization, `{"dataplane":"d"}`)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if len(f.calls) != 0 {
				t.Errorf("AssumeRole calls = %d, want 0", len(f.calls))
			}
		})
	}
}

// TestBrokerRejectsOIDCShapedBearer: this endpoint has no OIDC branch at
// all — ADR-040 decision 1 forbids a browser session minting credentials, so
// the bearer is never verified and never compared against the static token.
func TestBrokerRejectsOIDCShapedBearer(t *testing.T) {
	f := &fakeSTS{out: okCreds(time.Now().Add(time.Hour))}
	rec := postCredentials(brokerMux(f), "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1MSJ9.c2ln", `{"dataplane":"d"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (not 401 — the shape itself is the rejection)", rec.Code)
	}
	if len(f.calls) != 0 {
		t.Errorf("AssumeRole calls = %d, want 0", len(f.calls))
	}
}

func TestBrokerRejectsEmptyDataplane(t *testing.T) {
	bodies := []string{`{}`, `{"dataplane":""}`, `{"dataplane":"   "}`, `{"dataplane":`}
	for _, body := range bodies {
		f := &fakeSTS{out: okCreds(time.Now().Add(time.Hour))}
		rec := postCredentials(brokerMux(f), "Bearer "+brokerTestToken, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
		if len(f.calls) != 0 {
			t.Errorf("body %q: AssumeRole calls = %d, want 0", body, len(f.calls))
		}
	}
}

func TestBrokerRejectsUnsupportedProvider(t *testing.T) {
	f := &fakeSTS{out: okCreds(time.Now().Add(time.Hour))}
	rec := postCredentials(brokerMux(f), "Bearer "+brokerTestToken, `{"dataplane":"d","provider":"anthropic"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("provider anthropic: status = %d, want 400", rec.Code)
	}
	if len(f.calls) != 0 {
		t.Errorf("provider anthropic: AssumeRole calls = %d, want 0", len(f.calls))
	}

	for _, body := range []string{`{"dataplane":"d","provider":"bedrock"}`, `{"dataplane":"d"}`} {
		f := &fakeSTS{out: okCreds(time.Now().Add(time.Hour))}
		rec := postCredentials(brokerMux(f), "Bearer "+brokerTestToken, body)
		if rec.Code != 200 {
			t.Errorf("body %q: status = %d, want 200 (body: %s)", body, rec.Code, rec.Body.String())
		}
	}
}

func TestSanitizeSessionID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"host-01JABC", "host-01JABC"},
		{"my.host@dc1,eu", "my.host@dc1,eu"},
		{"a/b:c d", "a-b-c-d"},
		{"ok_+=,.@-", "ok_+=,.@-"},
	}
	for _, tc := range tests {
		if got := sanitizeSessionID(tc.in); got != tc.want {
			t.Errorf("sanitizeSessionID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Multi-byte UTF-8: every BYTE is replaced, so the output length equals
	// the input's byte length and is all '-'.
	got := sanitizeSessionID("üñî")
	if len(got) != len("üñî") {
		t.Errorf("sanitizeSessionID(üñî): len = %d, want %d", len(got), len("üñî"))
	}
	for i := 0; i < len(got); i++ {
		if got[i] != '-' {
			t.Errorf("sanitizeSessionID(üñî): byte %d = %q, want '-'", i, got[i])
		}
	}

	// The 64-char cap applies after replacement, not before.
	if got := sanitizeSessionID(strings.Repeat("a", 200)); len(got) != 64 {
		t.Errorf("200 x 'a': len = %d, want 64", len(got))
	}
	if got := sanitizeSessionID(strings.Repeat("/", 200)); len(got) != 64 {
		t.Errorf("200 x '/': len = %d, want 64", len(got))
	}
}

func TestBrokerSanitizesLongDataplaneID(t *testing.T) {
	f := &fakeSTS{out: okCreds(time.Now().Add(time.Hour))}
	body := `{"dataplane":"` + strings.Repeat("a", 80) + `/bad"}`
	rec := postCredentials(brokerMux(f), "Bearer "+brokerTestToken, body)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if len(f.calls) != 1 {
		t.Fatalf("AssumeRole calls = %d, want 1", len(f.calls))
	}
	in := f.calls[0]
	if in.RoleSessionName == nil || len(*in.RoleSessionName) != 64 {
		t.Errorf("RoleSessionName = %v, want a 64-char sanitized id", in.RoleSessionName)
	}
	if in.SourceIdentity == nil || *in.SourceIdentity != *in.RoleSessionName {
		t.Errorf("SourceIdentity = %v, want it equal to RoleSessionName", in.SourceIdentity)
	}
	if len(in.Tags) != 1 || *in.Tags[0].Value != *in.RoleSessionName {
		t.Errorf("tag value = %+v, want it equal to RoleSessionName", in.Tags)
	}
}

// TestBrokerRetriesWithoutSourceIdentity covers ADR-040 decision 2's
// inheritance caveat: when the task-role session already carries a
// SourceIdentity, AWS refuses a new one — the broker retries once WITHOUT it,
// so attribution degrades to session tags rather than the feature breaking.
func TestBrokerRetriesWithoutSourceIdentity(t *testing.T) {
	f := &fakeSTS{
		errs: []error{errors.New("AccessDenied: Cannot set a new SourceIdentity when one is already set")},
		out:  okCreds(time.Now().Add(time.Hour)),
	}
	rec := postCredentials(brokerMux(f), "Bearer "+brokerTestToken, `{"dataplane":"host-01JABC"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if len(f.calls) != 2 {
		t.Fatalf("AssumeRole calls = %d, want 2", len(f.calls))
	}
	if f.calls[0].SourceIdentity == nil {
		t.Error("call 1: SourceIdentity is nil, want non-nil")
	}
	if f.calls[1].SourceIdentity != nil {
		t.Error("call 2: SourceIdentity is non-nil, want nil (tag-only retry)")
	}
	retry := f.calls[1]
	if len(retry.Tags) != 1 || *retry.Tags[0].Key != "dataplane" || *retry.Tags[0].Value != "host-01JABC" {
		t.Errorf("call 2 tags = %+v, want the dataplane tag kept", retry.Tags)
	}
	if retry.DurationSeconds == nil || *retry.DurationSeconds != 3600 {
		t.Errorf("call 2 DurationSeconds = %v, want 3600", retry.DurationSeconds)
	}
}

// TestBrokerLatchesSourceIdentitySkip: inheritance is a per-boot environment
// fact, not a per-request one — after the first SourceIdentity refusal the
// broker latches and later mints go straight to the tags-only shape instead
// of re-paying the doomed first AssumeRole on every request.
func TestBrokerLatchesSourceIdentitySkip(t *testing.T) {
	f := &fakeSTS{
		errs: []error{errors.New("AccessDenied: Cannot set a new SourceIdentity when one is already set")},
		out:  okCreds(time.Now().Add(time.Hour)),
	}
	mux := brokerMux(f) // ONE server across both requests — the latch lives on it
	if rec := postCredentials(mux, "Bearer "+brokerTestToken, `{"dataplane":"host-01JABC"}`); rec.Code != 200 {
		t.Fatalf("first mint: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if rec := postCredentials(mux, "Bearer "+brokerTestToken, `{"dataplane":"host-01JABC"}`); rec.Code != 200 {
		t.Fatalf("second mint: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if len(f.calls) != 3 {
		t.Fatalf("AssumeRole calls = %d, want 3 (2 for the first mint's retry, 1 for the latched second)", len(f.calls))
	}
	if f.calls[2].SourceIdentity != nil {
		t.Error("call 3: SourceIdentity is non-nil — the latch did not stick")
	}
	if len(f.calls[2].Tags) != 1 || *f.calls[2].Tags[0].Value != "host-01JABC" {
		t.Errorf("call 3 tags = %+v, want the dataplane tag kept", f.calls[2].Tags)
	}
}

func TestBrokerDoesNotRetryOtherErrors(t *testing.T) {
	f := &fakeSTS{err: errors.New("AccessDenied: not authorized to perform sts:AssumeRole")}
	rec := postCredentials(brokerMux(f), "Bearer "+brokerTestToken, `{"dataplane":"d"}`)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if len(f.calls) != 1 {
		t.Errorf("AssumeRole calls = %d, want 1 (no blind retry loop)", len(f.calls))
	}
}

func TestBrokerSTSFailureBodyIsFixed(t *testing.T) {
	f := &fakeSTS{err: errors.New("AccessDenied: user arn:aws:sts::123456789012:assumed-role/secret-role-name is not authorized")}
	bs := NewBrokerServer(brokerTestToken, brokerTestARN, f)
	var sink []error
	bs.OnError = func(err error) { sink = append(sink, err) }
	mux := http.NewServeMux()
	bs.Mount(mux)

	rec := postCredentials(mux, "Bearer "+brokerTestToken, `{"dataplane":"d"}`)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"secret-role-name", "AccessDenied", "assumed-role"} {
		if strings.Contains(body, leak) {
			t.Errorf("response body leaks STS error text %q: %s", leak, body)
		}
	}
	// Server-side logging is required — silence would be worse.
	found := false
	for _, err := range sink {
		if strings.Contains(err.Error(), "AccessDenied") {
			found = true
		}
	}
	if !found {
		t.Errorf("OnError sink never received the STS error: %v", sink)
	}
}

func TestBrokerIncompleteCredentialsIs502(t *testing.T) {
	outputs := []*sts.AssumeRoleOutput{
		{Credentials: nil},
		{Credentials: &ststypes.Credentials{
			AccessKeyId:     aws.String("ASIAFAKE"),
			SecretAccessKey: aws.String("fake-secret"),
			SessionToken:    nil,
			Expiration:      aws.Time(time.Now().Add(time.Hour)),
		}},
	}
	for i, out := range outputs {
		f := &fakeSTS{out: out}
		rec := postCredentials(brokerMux(f), "Bearer "+brokerTestToken, `{"dataplane":"d"}`)
		if rec.Code != http.StatusBadGateway {
			t.Errorf("case %d: status = %d, want 502", i, rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "accessKeyId") {
			t.Errorf("case %d: response is a partially populated credential object: %s", i, body)
		}
	}
}

// TestBrokerEmptyTokenRefusesEverything: unlike authn there is NO
// loopback/unauthenticated waiver on this route — an empty configured token
// means "refuse everything", never "serve unauthenticated".
func TestBrokerEmptyTokenRefusesEverything(t *testing.T) {
	for _, authorization := range []string{"", "Bearer ", "Bearer anything", "Bearer " + brokerTestToken} {
		f := &fakeSTS{out: okCreds(time.Now().Add(time.Hour))}
		mux := http.NewServeMux()
		NewBrokerServer("", brokerTestARN, f).Mount(mux)
		rec := postCredentials(mux, authorization, `{"dataplane":"d"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("authorization %q: status = %d, want 401", authorization, rec.Code)
		}
		if len(f.calls) != 0 {
			t.Errorf("authorization %q: AssumeRole calls = %d, want 0", authorization, len(f.calls))
		}
	}
}
