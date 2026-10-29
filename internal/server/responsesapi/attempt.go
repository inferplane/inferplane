package responsesapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/inferplane/inferplane/internal/audit"
	"github.com/inferplane/inferplane/internal/governance"
	"github.com/inferplane/inferplane/internal/keystore"
	"github.com/inferplane/inferplane/internal/pricing"
	"github.com/inferplane/inferplane/internal/responses"
	"github.com/inferplane/inferplane/internal/router"
	"github.com/inferplane/inferplane/internal/server/requestpolicy"
	"github.com/inferplane/inferplane/pkg/schema"
	"github.com/inferplane/inferplane/providers"
)

type attempt struct {
	h         *Handler
	req       *http.Request
	principal keystore.Principal
	target    router.ChainTarget
	proxy     *providers.ProxyRequest
	table     *pricing.Table
	started   time.Time
	admission *governance.Admission
	errorCode string // bounded, allowlisted upstream failure evidence
	inputBody []byte // sanitized client wire; proxy.RawBody may be translated
}

func errorStatus(err error) int {
	var upstream *providers.UpstreamError
	if errors.As(err, &upstream) {
		return upstream.HTTPStatus()
	}
	return http.StatusBadGateway
}
func retryStatus(status int) bool { return status == 429 || status >= 500 }

func (a attempt) complete(w http.ResponseWriter, last bool) bool {
	audit.BeginAttempt(a.req.Context(), a.target.ProviderName, a.target.Model)
	resp, err := a.target.Provider.Complete(a.req.Context(), a.proxy)
	if err != nil || resp == nil {
		status := errorStatus(err)
		a.errorCode = upstreamCode(err)
		retry := !last && retryStatus(status) && a.req.Context().Err() == nil
		a.endFailedAttempt(err, 0, retry)
		a.finish(status, nil, nil, false, 0)
		if retry {
			return true
		}
		writeCodedError(w, status, "upstream request failed", a.errorCode)
		return false
	}
	if resp.StatusCode/100 != 2 {
		var observed *schema.Usage
		if resp.Parsed != nil {
			observed = resp.Parsed.Usage
		}
		a.errorCode = providers.ResponseErrorCode(resp.RawBody)
		retry := !last && retryStatus(resp.StatusCode)
		if retry {
			requestpolicy.EndRetriedAttempt(a.req.Context(), nil, resp.StatusCode, false)
		} else {
			audit.EndOpenAttempt(a.req.Context(), requestpolicy.AttemptStatusForHTTP(resp.StatusCode), resp.StatusCode)
		}
		a.finish(resp.StatusCode, observed, nil, false, 0)
		if retry {
			return true
		}
		writeCodedError(w, resp.StatusCode, "upstream request failed", a.errorCode)
		return false
	}
	// The provider exchange is over: close the attempt before rendering,
	// settlement, body capture and the record write. An unusable 2xx body is
	// the gateway's verdict (the 502 below), not the upstream's status.
	if resp.Parsed == nil || resp.Parsed.Usage == nil {
		audit.EndOpenAttempt(a.req.Context(), audit.AttemptUpstreamError, resp.StatusCode)
		a.finish(502, nil, nil, false, 0)
		writeError(w, 502, "upstream_error", "upstream response omitted usage")
		return false
	}
	body := resp.RawBody
	if a.target.Provider.Name() != "openai_responses" {
		body, err = responses.CanonicalToResponseForRequest(resp.Parsed, a.proxy.Parsed)
	}
	if err != nil || len(body) == 0 {
		// A provider successfully processed this request. Settle its observed
		// usage even if the output cannot be rendered; do not bill a retry.
		audit.EndOpenAttempt(a.req.Context(), audit.AttemptUpstreamError, resp.StatusCode)
		a.finish(502, resp.Parsed.Usage, nil, false, 0)
		writeError(w, 502, "upstream_error", "upstream response is incompatible")
		return false
	}
	audit.EndOpenAttempt(a.req.Context(), requestpolicy.AttemptStatusForHTTP(resp.StatusCode), resp.StatusCode)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, writeErr := w.Write(body)
	a.h.r.RecordResult(a.target.ProviderName, a.target.Identity, true)
	if writeErr == nil {
		requestpolicy.RecordSuccess(a.req)
	}
	a.finish(resp.StatusCode, resp.Parsed.Usage, body, writeErr != nil, 0)
	return false
}

func (a attempt) stream(w http.ResponseWriter, last bool) bool {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "api_error", "streaming is unavailable")
		a.finish(500, nil, nil, false, 0)
		return false
	}
	audit.BeginAttempt(a.req.Context(), a.target.ProviderName, a.target.Model)
	seq, err := a.target.Provider.Stream(a.req.Context(), a.proxy)
	if err != nil || seq == nil {
		status := errorStatus(err)
		a.errorCode = streamFailureCode(err)
		retry := !last && retryStatus(status) && a.req.Context().Err() == nil
		a.endFailedAttempt(err, 0, retry)
		a.finish(status, nil, nil, false, 0)
		if retry {
			return true
		}
		writeCodedError(w, status, "upstream stream failed", a.errorCode)
		return false
	}
	native := a.target.Provider.Name() == "openai_responses"
	renderer := responses.NewStreamState(a.target.Model, a.proxy.Parsed)
	var usage *schema.Usage
	var ttft float64
	var conversionErr error
	committed, terminal, failed := false, false, false
	commit := func() {
		if !committed {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(200)
			committed = true
			ttft = time.Since(a.started).Seconds()
			audit.MarkFirstToken(a.req.Context())
		}
	}
	fail := func(cause error) bool {
		a.errorCode = streamFailureCode(cause)
		recovery := a.errorCode == "context_length_exceeded" || a.errorCode == "invalid_encrypted_content"
		// Native terminal failures must retain the SSE recovery shape consumed
		// by Codex even when they are the first upstream event.
		if !committed && native && recovery && errors.Is(cause, responses.ErrFailed) {
			commit()
		}
		if !committed {
			status := errorStatus(cause)
			retry := conversionErr == nil && !last && retryStatus(status) && a.req.Context().Err() == nil
			a.endFailedAttempt(cause, 0, retry)
			a.finish(status, usage, nil, false, ttft)
			if retry {
				return true
			}
			writeCodedError(w, status, "upstream stream failed", a.errorCode)
			return false
		}
		// Never expose arbitrary provider error strings or restart a committed
		// stream on another model. Output already observed is still settled.
		requestpolicy.EndStreamAttempt(a.req.Context(), false, cause)
		if !errors.Is(cause, errClientWriteFailed) {
			data, _ := json.Marshal(map[string]string{
				"type": "error", "code": a.errorCode, "message": "upstream stream interrupted",
			})
			event := responses.Event{Type: "error", Data: data}
			if recovery {
				event.Type = "response.failed"
				event.Data, _ = json.Marshal(map[string]any{
					"type": "response.failed",
					"response": map[string]any{"object": "response", "status": "failed", "output": []any{},
						"error": map[string]string{"code": a.errorCode, "message": "upstream stream interrupted"}},
				})
			}
			if err := responses.WriteEvent(w, event); err != nil {
				a.errorCode = streamFailureCode(errClientWriteFailed)
			} else {
				flusher.Flush()
			}
		}
		a.finish(200, usage, nil, true, ttft)
		return false
	}
	for event, readErr := range seq {
		if readErr != nil {
			if errors.Is(readErr, responses.ErrTruncated) {
				readErr = errors.Join(errStreamIncomplete, readErr)
			}
			return fail(readErr)
		}
		if event == nil {
			continue
		}
		if event.Chunk != nil {
			if event.Chunk.Message != nil {
				usage = schema.MergeUsage(usage, event.Chunk.Message.Usage)
			}
			usage = schema.MergeUsage(usage, event.Chunk.Usage)
			terminal = terminal || event.Chunk.Type == "message_stop"
			failed = failed || event.Chunk.Type == "error"
		}
		if conversionErr != nil {
			// A conversion failure can precede the terminal usage frame (for
			// example a truncated tool argument at a synthesized block-stop).
			// Keep consuming accounting under the same cancellable request;
			// never emit a successful completion or start another attempt.
			continue
		}
		if native {
			if len(event.Raw) > 0 {
				commit()
				if _, err := w.Write(event.Raw); err != nil {
					return fail(errClientWriteFailed)
				}
				flusher.Flush()
			}
		} else if event.Chunk != nil {
			events, err := renderer.Convert(event.Chunk)
			if err != nil {
				conversionErr = errResponseIncompatible
				if terminal && usage == nil {
					conversionErr = errUsageMissing
				}
				continue
			}
			for _, converted := range events {
				commit()
				if err := responses.WriteEvent(w, converted); err != nil {
					return fail(errClientWriteFailed)
				}
			}
			if len(events) > 0 {
				flusher.Flush()
			}
		}
	}
	if conversionErr != nil {
		return fail(conversionErr)
	}
	if !native {
		if _, err := renderer.Finish(); err != nil {
			if errors.Is(err, responses.ErrTruncated) {
				return fail(errStreamIncomplete)
			}
			return fail(errResponseIncompatible)
		}
	}
	if !committed || !terminal || failed {
		return fail(errStreamIncomplete)
	}
	if usage == nil {
		return fail(errUsageMissing)
	}
	a.h.r.RecordResult(a.target.ProviderName, a.target.Identity, true)
	requestpolicy.RecordSuccess(a.req)
	requestpolicy.EndStreamAttempt(a.req.Context(), true, nil)
	a.finish(200, usage, nil, false, ttft)
	return false
}

// endFailedAttempt closes the open attempt of a failed provider exchange:
// retried failures keep the upstream status and never a model-not-found
// verdict (this loop retries only 429/5xx), terminal ones use the error's own
// status. The gateway's synthesized status is never written.
func (a attempt) endFailedAttempt(err error, httpStatus int, retried bool) {
	if retried {
		requestpolicy.EndRetriedAttempt(a.req.Context(), err, httpStatus, false)
		return
	}
	requestpolicy.EndAttemptOnError(a.req.Context(), err)
}

// Never copy a provider's raw error message into the client response.
func upstreamCode(err error) string {
	var upstream *providers.UpstreamError
	if errors.As(err, &upstream) {
		return providers.ResponseErrorCode(upstream.Body)
	}
	return ""
}

func writeCodedError(w http.ResponseWriter, status int, message, code string) {
	if code == "" {
		writeError(w, status, "upstream_error", message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	errorType := "upstream_error"
	if code == "context_length_exceeded" || code == "invalid_encrypted_content" {
		errorType = "invalid_request_error"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"type": errorType, "code": code, "param": nil, "message": message,
	}})
}
