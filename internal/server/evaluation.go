package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inferplane/inferplane/internal/adminauth"
	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
	"github.com/inferplane/inferplane/pkg/schema"
)

// WithEvaluations mounts an administrator-only, same-process comparison bridge.
// Every call ALSO needs a real virtual key and traverses the unchanged DataMux:
// admin authority can never replace inference identity, readiness or budgets.
// There is no client-selected host, background job, retry or stored credential.
func WithEvaluations(admin, data http.Handler, tokens []string, verifier OIDCVerifier, mapping adminauth.MappingConfig, aud *audit.Writer) http.Handler {
	var emit func(audit.Record)
	if aud != nil {
		emit = aud.Append
	}
	mux := http.NewServeMux()
	mux.Handle("POST /admin/evaluations", AdminAuth(tokens, verifier, mapping, adminDenialEmitter(emit),
		requireAdmin(evaluationHandler(data), emit)))
	mux.Handle("/", admin)
	return mux
}

type evaluationRequest struct {
	Protocol string          `json:"protocol"`
	Model    string          `json:"model"`
	Body     json.RawMessage `json:"body"`
}

type evaluationResult struct {
	Protocol          string          `json:"protocol"`
	Model             string          `json:"model"`
	HTTPStatus        int             `json:"http_status"`
	ResponseSuccess   bool            `json:"response_success"`
	Outcome           string          `json:"outcome"`
	StopReason        string          `json:"stop_reason,omitempty"`
	DurationMS        float64         `json:"duration_ms"`
	TTFTMS            *float64        `json:"ttft_ms"` // not observed on this non-streaming UI
	Output            string          `json:"output"`
	Usage             *audit.UsageRef `json:"usage"`
	Cost              *audit.CostRef  `json:"cost"`
	Settled           bool            `json:"settled"`
	AccountingKnown   bool            `json:"accounting_known"`
	SelectedModel     string          `json:"selected_model,omitempty"`
	Fallback          string          `json:"fallback,omitempty"`
	Attempts          int             `json:"attempts"`
	UnsettledAttempts int             `json:"unsettled_attempts"`
}

func evaluationHandler(data http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		bad := func() {
			http.Error(w, `{"error":"invalid evaluation request or missing virtual key"}`, http.StatusBadRequest)
		}
		key := strings.TrimSpace(r.Header.Get("X-Inferplane-Virtual-Key"))
		if key == "" || strings.ContainsAny(key, "\r\n") {
			bad()
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, defaultMaxRequestBytes))
		decoder.DisallowUnknownFields()
		var input evaluationRequest
		if decoder.Decode(&input) != nil || input.Model == "" {
			bad()
			return
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			bad()
			return
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(input.Body, &body) != nil || body == nil {
			bad()
			return
		}
		// This initial UI measures end-to-end, non-streaming requests. Reject
		// stream/background modes rather than silently changing client bytes.
		for _, field := range []string{"stream", "background"} {
			if value, exists := body[field]; exists && string(value) != "false" {
				bad()
				return
			}
		}
		path := ""
		switch input.Protocol {
		case "bedrock-converse":
			path = "/model/" + url.PathEscape(input.Model) + "/converse"
		case "anthropic":
			path = "/v1/messages"
		case "openai":
			path = "/v1/chat/completions"
		case "responses":
			path = "/v1/responses"
		default:
			bad()
			return
		}
		if input.Protocol != "bedrock-converse" {
			var model string
			if json.Unmarshal(body["model"], &model) != nil || model != input.Model {
				bad()
				return
			}
		}
		result := evaluationResult{Protocol: input.Protocol, Model: input.Model}
		settlements := 0
		ctx := requestpolicy.WithAttemptObserver(r.Context(), func() { result.Attempts++ })
		ctx = requestpolicy.WithSettlementObserver(ctx, func(cost *audit.CostRef, usage *schema.Usage, complete bool) {
			settlements++
			result.Cost, result.Usage, result.Settled = cost, audit.UsageFrom(usage), complete
			result.AccountingKnown = complete && cost != nil && !cost.PricingMissing &&
				result.Usage != nil && result.Usage.Evidence != nil && !result.Usage.Evidence.AccountingUncertain
		})
		call, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(input.Body))
		if err != nil {
			bad()
			return
		}
		call.Header.Set("Authorization", "Bearer "+key)
		call.Header.Set("Content-Type", "application/json")
		call.Header.Set("Anthropic-Version", "2023-06-01")
		// Fresh headers intentionally exclude the admin token, cookies and the
		// virtual-key transport header. KeyAuth alone resolves the principal.
		record := &evaluationRecorder{header: make(http.Header)}
		start := time.Now()
		data.ServeHTTP(record, call)
		result.UnsettledAttempts = max(0, result.Attempts-settlements)
		// A complete final response is not proof that failed earlier attempts
		// were free. Never present last-attempt cost as a known request total.
		if result.Attempts != 1 || settlements != 1 {
			result.AccountingKnown = false
		}
		result.DurationMS = float64(time.Since(start).Nanoseconds()) / 1e6
		result.HTTPStatus = record.status
		if result.HTTPStatus == 0 {
			result.HTTPStatus = http.StatusOK
		}
		result.SelectedModel = record.header.Get("X-Inferplane-Substituted-Model")
		if value := record.header.Get("X-Inferplane-Model-Fallback"); value != "" {
			result.SelectedModel = value
		}
		result.Fallback = record.header.Get("X-Inferplane-Fallback")
		result.Output, result.StopReason, result.Outcome = evaluationOutput(input.Protocol, record.body.Bytes())
		if record.overflow {
			result.Outcome = "response_too_large"
		}
		if result.HTTPStatus < 200 || result.HTTPStatus >= 300 {
			result.Outcome = "http_error"
			result.Output = "" // do not echo arbitrary upstream error content
		}
		result.ResponseSuccess = result.Outcome == "completed"
		_ = json.NewEncoder(w).Encode(result)
	})
}

// Bound the browser-visible response without disrupting completion/settlement.
// This is not a network stream and does not implement Flusher.
type evaluationRecorder struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (r *evaluationRecorder) Header() http.Header { return r.header }
func (r *evaluationRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}
func (r *evaluationRecorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	const limit = 4 << 20
	if len(b) > limit-r.body.Len() {
		r.overflow = true
	} else if !r.overflow {
		r.body.Write(b)
	}
	return len(b), nil
}

func evaluationOutput(protocol string, raw []byte) (string, string, string) {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil || body["error"] != nil {
		return "", "", "protocol_error"
	}
	var stop, text string
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Refusal string          `json:"refusal"`
		ToolUse json.RawMessage `json:"toolUse"`
	}
	tool, refusal := false, false
	switch protocol {
	case "bedrock-converse":
		var output struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(body["output"], &output)
		_ = json.Unmarshal(output.Message.Content, &blocks)
		_ = json.Unmarshal(body["stopReason"], &stop)
	case "anthropic":
		_ = json.Unmarshal(body["content"], &blocks)
		_ = json.Unmarshal(body["stop_reason"], &stop)
	case "openai":
		var choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content   string            `json:"content"`
				Refusal   string            `json:"refusal"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		}
		if json.Unmarshal(body["choices"], &choices) == nil && len(choices) == 1 {
			stop, text = choices[0].Finish, choices[0].Message.Content
			tool, refusal = len(choices[0].Message.ToolCalls) > 0, choices[0].Message.Refusal != ""
		}
	case "responses":
		_ = json.Unmarshal(body["status"], &stop)
		var output []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(body["output"], &output) == nil {
			for _, item := range output {
				tool = tool || item.Type == "function_call"
				var content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				_ = json.Unmarshal(item.Content, &content)
				for _, block := range content {
					text += block.Text
					refusal = refusal || block.Type == "refusal"
				}
			}
		}
	}
	for _, block := range blocks {
		text += block.Text
		tool = tool || block.Type == "tool_use" || block.ToolUse != nil
		refusal = refusal || block.Type == "refusal" || block.Refusal != ""
	}
	outcome := "protocol_error"
	switch stop {
	case "end_turn", "stop", "stop_sequence", "tool_use", "tool_calls", "completed":
		outcome = "completed"
	case "max_tokens", "length", "model_context_window_exceeded", "incomplete":
		outcome = "truncated"
	case "refusal", "content_filtered", "content_filter", "guardrail_intervened":
		outcome = "refused"
	}
	if refusal {
		outcome = "refused"
	}
	if outcome == "completed" && strings.TrimSpace(text) == "" && !tool {
		outcome = "empty_response"
	}
	return text, stop, outcome
}
