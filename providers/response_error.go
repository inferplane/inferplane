package providers

import "encoding/json"

// ResponseErrorCode returns only known Responses protocol error codes. Never
// return upstream messages, parameter names, unknown codes, or arbitrary text:
// those can contain prompts or credentials. Both transports and ingresses must
// apply this allowlist rather than trusting a provider's raw error envelope.
func ResponseErrorCode(body []byte) string {
	if len(body) > 64<<10 {
		return ""
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	switch envelope.Error.Code {
	case "context_length_exceeded", "invalid_encrypted_content":
		return envelope.Error.Code
	default:
		return ""
	}
}
