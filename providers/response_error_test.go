package providers

import "testing"

func TestResponseErrorCode(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"error":{"code":"context_length_exceeded","message":"private prompt"}}`, "context_length_exceeded"},
		{`{"error":{"code":"invalid_encrypted_content"}}`, "invalid_encrypted_content"},
		{`{"error":{"code":"private prompt"}}`, ""},
		{`{"error":{"message":"context_length_exceeded"}}`, ""},
		{`{"error":{"code":123}}`, ""},
		{`{"error":`, ""},
	} {
		if got := ResponseErrorCode([]byte(tc.body)); got != tc.want {
			t.Fatalf("code=%q want=%q", got, tc.want)
		}
	}
}
