package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// handleChat is the data path: decode the wire schema strictly, hand the
// core the domain request, encode whatever it decides. No admission logic
// lives here — auth, quotas, slots, routing and failover are the core's.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	wire, apiErr := decodeChatRequest(r)
	if apiErr != nil {
		writeError(w, r, apiErr)

		return
	}

	result, err := s.chat.Chat(r.Context(), apiKeyFrom(r.Context()), wire.toDomain())
	if err != nil {
		s.writeChatError(w, r, err)

		return
	}

	if result.Substituted {
		// Disclosure is unconditional (decision #5): the header flags it,
		// and the body's model names what actually served.
		w.Header().Set("X-Model-Substituted", "true")
	}

	writeJSON(w, http.StatusOK, chatResponse(result))
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.health.Live(); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)

		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.health.Ready(r.Context()); err != nil {
		// The probe body stays terse; the refusal reason reaches the log
		// through the HealthChecker decorator wrapped around s.health.
		w.WriteHeader(http.StatusServiceUnavailable)

		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) writeChatError(w http.ResponseWriter, r *http.Request, err error) {
	var gwErr *gateway.Error
	switch {
	case errors.As(err, &gwErr):
		writeError(w, r, fromGatewayError(gwErr))
	case errors.Is(err, context.Canceled):
		w.WriteHeader(statusClientClosedRequest)
	default:
		// The core's contract is *gateway.Error or a disconnect; anything
		// else is a bug the caller gets no detail about — the ChatService
		// decorator wrapped around s.chat gives it its log line.
		writeError(w, r, &apiError{
			status:  http.StatusInternalServerError,
			code:    codeInternalError,
			message: "internal error",
		})
	}
}

// fromGatewayError translates the core's typed failure into wire terms: the
// status for its code, and the code-specific details openapi.yaml promises.
func fromGatewayError(e *gateway.Error) *apiError {
	ae := &apiError{status: statusFor(e.Code), code: string(e.Code), message: e.Message}

	var d errorDetails
	if e.RetryAfter > 0 {
		d.RetryAfterSeconds = ceilSeconds(e.RetryAfter)
	}
	if e.Quota != nil {
		d.Quota = &quotaWire{Limit: e.Quota.Limit, WindowSeconds: e.Quota.WindowSeconds, Used: e.Quota.Used}
	}
	if e.MaxInFlight > 0 {
		d.MaxInFlight = e.MaxInFlight
	}
	if e.RequestedModel != "" {
		d.RequestedModel = e.RequestedModel
	}
	if d != (errorDetails{}) {
		ae.details = &d
	}

	return ae
}

func statusFor(code gateway.ErrorCode) int {
	switch code {
	case gateway.CodeInvalidRequest:
		return http.StatusBadRequest
	case gateway.CodeUnauthorized:
		return http.StatusUnauthorized
	case gateway.CodeQuotaExceeded, gateway.CodeConcurrencyCeiling:
		return http.StatusTooManyRequests
	case gateway.CodeUpstreamFailed:
		return http.StatusBadGateway
	case gateway.CodeModelUnavailable:
		return http.StatusServiceUnavailable
	case gateway.CodeGatewayTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// ceilSeconds rounds up: telling a caller to retry a moment too late is
// honest; a moment too early re-refuses them.
func ceilSeconds(d time.Duration) int {
	return int((d + time.Second - 1) / time.Second)
}
