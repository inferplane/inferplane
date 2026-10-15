package responsesapi

import (
	"encoding/json"
	"errors"
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
