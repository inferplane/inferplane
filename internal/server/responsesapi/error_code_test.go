package responsesapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/providers"
)

func TestUpstreamRecoveryCodeIsAllowlisted(t *testing.T) {
	for _, code := range []string{"context_length_exceeded", "invalid_encrypted_content", "private-value"} {
		err := &providers.UpstreamError{StatusCode: 400, Body: []byte(`{"error":{"code":"` + code + `","message":"private-value"}}`)}
		rec := httptest.NewRecorder()
		writeCodedError(rec, errorStatus(err), "upstream stream failed", upstreamCode(err))
		var got struct {
			Error struct {
				Code *string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &got) != nil || rec.Code != 400 {
			t.Fatal(rec)
		}
		if code == "private-value" {
			if got.Error.Code != nil {
				t.Fatal("unknown code leaked")
			}
		} else if got.Error.Code == nil || *got.Error.Code != code {
			t.Fatal("lost recovery code")
		}
		if strings.Contains(rec.Body.String(), "private-value") {
			t.Fatal("error leaked")
		}
	}
	if upstreamCode(errors.New("context_length_exceeded")) != "" {
		t.Fatal("classified arbitrary text")
	}
}

func TestGatewayStreamErrorKeepsUpstreamErrorType(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCodedError(rec, 502, "upstream stream failed", "upstream_timeout")
	var payload struct {
		Error struct{ Type, Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Type != "upstream_error" || payload.Error.Code != "upstream_timeout" {
		t.Fatalf("gateway failure misclassified as invalid request: %s", rec.Body.String())
	}
}

func TestStreamFailureCodeUsesTypedEvidenceInOrder(t *testing.T) {
	typed := &providers.UpstreamError{StatusCode: 400, Body: []byte(`{"error":{"code":"invalid_encrypted_content"}}`)}
	unknown := &providers.UpstreamError{StatusCode: 400, Body: []byte(`{"error":{"code":"request_canceled"}}`)}
	for _, tc := range []struct {
		name  string
		cause error
		want  string
	}{
		{"wrapped-provider", fmt.Errorf("provider: %w", typed), "invalid_encrypted_content"},
		{"provider-before-cancel", errors.Join(context.Canceled, typed), "invalid_encrypted_content"},
		{"unknown-provider-before-deadline", errors.Join(unknown, context.DeadlineExceeded), "upstream_timeout"},
		{"cancel-before-deadline", errors.Join(context.DeadlineExceeded, context.Canceled), "request_canceled"},
		{"deadline-before-sentinel", errors.Join(errStreamIncomplete, context.DeadlineExceeded), "upstream_timeout"},
		{"wrapped-incomplete", fmt.Errorf("read: %w", errStreamIncomplete), "stream_incomplete"},
		{"wrapped-incompatible", fmt.Errorf("render: %w", errResponseIncompatible), "response_incompatible"},
		{"wrapped-missing-usage", fmt.Errorf("usage: %w", errUsageMissing), "usage_missing"},
		{"wrapped-client-write", fmt.Errorf("write: %w", errClientWriteFailed), "client_write_failed"},
		{"sentinel-text-only", errors.New("client write failed"), "upstream_error"},
		{"missing-cause", nil, "upstream_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamFailureCode(tc.cause); got != tc.want {
				t.Fatalf("bounded evidence=%q want %q", got, tc.want)
			}
		})
	}
}
