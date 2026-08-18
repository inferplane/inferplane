package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCredentialFetcherHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1alpha1/credentials" {
			t.Errorf("path = %q, want /v1alpha1/credentials", r.URL.Path)
		}
		var req struct {
			Dataplane string `json:"dataplane"`
			Provider  string `json:"provider"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Dataplane != "host-1" {
			t.Errorf("dataplane = %q, want host-1", req.Dataplane)
		}
		if req.Provider != "bedrock" {
			t.Errorf("provider = %q, want bedrock", req.Provider)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessKeyId":"ASIAFAKE","secretAccessKey":"shh","sessionToken":"tok","expiration":"2099-01-02T03:04:05Z"}`))
	}))
	defer srv.Close()

	f := &CredentialFetcher{URL: srv.URL, BrokerToken: "broker-tok", Dataplane: "host-1"}
	id, secret, session, expires, err := f.Credentials(t.Context())
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if id != "ASIAFAKE" {
		t.Errorf("id = %q, want ASIAFAKE", id)
	}
	if secret != "shh" {
		t.Errorf("secret = %q, want shh", secret)
	}
	if session != "tok" {
		t.Errorf("session = %q, want tok", session)
	}
	if want := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC); !expires.Equal(want) {
		t.Errorf("expires = %v, want %v", expires, want)
	}
}

func TestCredentialFetcherSendsBrokerToken(t *testing.T) {
	// The heartbeat token must never appear on this channel (ADR-040
	// decision 1): the fetcher carries ONLY the dedicated broker token, and
	// this test asserts the Authorization header is exactly that token.
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"accessKeyId":"a","secretAccessKey":"b","sessionToken":"c","expiration":"2099-01-02T03:04:05Z"}`))
	}))
	defer srv.Close()

	f := &CredentialFetcher{URL: srv.URL, BrokerToken: "broker-tok", Dataplane: "host-1"}
	if _, _, _, _, err := f.Credentials(t.Context()); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if gotAuth != "Bearer broker-tok" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer broker-tok")
	}
}

func TestCredentialFetcherErrorOmitsResponseBody(t *testing.T) {
	// ADR-040 decision 1's secret-hygiene requirement, and the assertion that
	// makes it checkable: a non-2xx error carries the status code plus a
	// fixed string, and NONE of the response body — which here contains three
	// credential-looking canaries — may reach the error text.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"denied","leak":"AKIA_CANARY_ID SECRET_CANARY_VALUE role arn:aws:iam::123456789012:role/canary-role"}`))
	}))
	defer srv.Close()

	f := &CredentialFetcher{URL: srv.URL, BrokerToken: "broker-tok", Dataplane: "host-1"}
	_, _, _, _, err := f.Credentials(t.Context())
	if err == nil {
		t.Fatal("Credentials: want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "403") {
		t.Errorf("error %q does not contain the status code 403", msg)
	}
	for _, canary := range []string{
		"AKIA_CANARY_ID",
		"SECRET_CANARY_VALUE",
		"role arn:aws:iam::123456789012:role/canary-role",
	} {
		if strings.Contains(msg, canary) {
			t.Errorf("error %q leaks response-body canary %q", msg, canary)
		}
	}
}

func TestCredentialFetcherMalformedJSON(t *testing.T) {
	// The decode error is a fixed string instead of %w precisely because
	// encoding/json errors can quote the offending bytes — i.e. the response
	// body, i.e. potentially a credential.
	//
	// The two bodies are NOT interchangeable, and only the second one actually
	// pins the no-%w rule. Verified against this repo's Go: a truncated object
	// yields a bare "unexpected EOF", and every SyntaxError/UnmarshalTypeError
	// this decoder can produce is terse ("invalid character '<' ...", "cannot
	// unmarshal number into Go struct field ... of type string") — none echoes
	// the payload. time.Time's UnmarshalJSON is the exception: it reports
	//   parsing time "<value>" as "2006-01-02T15:04:05Z07:00": cannot parse "<value>" as "2006"
	// echoing the raw string TWICE. `expiration` is a field the credential
	// response genuinely carries, so a %w here would put attacker- or
	// broker-supplied response bytes into a mayu error string.
	for _, body := range []string{
		`{"accessKeyId": "AKIA_CANARY_ID", `, // truncated: any decode failure must error
		`{"expiration":"AKIA_CANARY_ID"}`,    // time-parse failure: the error DOES echo the value
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		f := &CredentialFetcher{URL: srv.URL, BrokerToken: "broker-tok", Dataplane: "host-1"}
		_, _, _, _, err := f.Credentials(t.Context())
		srv.Close()
		if err == nil {
			t.Fatalf("body %q: Credentials: want error, got nil", body)
		}
		if strings.Contains(err.Error(), "AKIA_CANARY_ID") {
			t.Fatalf("body %q: error %q leaks response-body bytes", body, err.Error())
		}
	}
}

func TestCredentialFetcherIncompleteResponse(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"accessKeyId":"a","secretAccessKey":"b","sessionToken":"c"}`,
		`{"accessKeyId":"","secretAccessKey":"b","sessionToken":"c","expiration":"2099-01-02T03:04:05Z"}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		f := &CredentialFetcher{URL: srv.URL, BrokerToken: "broker-tok", Dataplane: "host-1"}
		_, _, _, _, err := f.Credentials(t.Context())
		srv.Close()
		if err == nil {
			t.Errorf("body %q: want error, got nil — a 200 with a partial body must not become empty AWS credentials", body)
		}
	}
}

func TestCredentialFetcherDoesNotFollowRedirects(t *testing.T) {
	// Following the redirect would ship the broker token to whatever host the
	// redirect names, so the fetcher must surface the 302 as a non-2xx error
	// and never call the target.
	targetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalled = true
		_, _ = w.Write([]byte(`{"accessKeyId":"a","secretAccessKey":"b","sessionToken":"c","expiration":"2099-01-02T03:04:05Z"}`))
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1alpha1/credentials", http.StatusFound)
	}))
	defer srv.Close()

	f := &CredentialFetcher{URL: srv.URL, BrokerToken: "broker-tok", Dataplane: "host-1"}
	_, _, _, _, err := f.Credentials(t.Context())
	if err == nil {
		t.Fatal("Credentials: want error on redirect, got nil")
	}
	if targetCalled {
		t.Fatal("redirect target was called — the fetcher followed a redirect")
	}
}

func TestCredentialFetcherUnreachableControlPlane(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	f := &CredentialFetcher{URL: url, BrokerToken: "broker-tok", Dataplane: "host-1"}
	_, _, _, _, err := f.Credentials(t.Context())
	if err == nil {
		t.Fatal("Credentials: want error, got nil")
	}
	if !strings.Contains(err.Error(), "credential fetch") {
		t.Fatalf("error %q does not contain %q", err.Error(), "credential fetch")
	}
}
