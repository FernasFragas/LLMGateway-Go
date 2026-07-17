// Package api is the HTTP adapter: the edge that owns the wire contract in
// openapi.yaml and translates it into the core's vocabulary. It imports
// internal/gateway — never the reverse.
//
// One concern per file:
//
//   - server.go: the Server — lifecycle (Start/Shutdown) and the routing
//     table, with nothing about any one endpoint;
//   - handler.go: endpoint logic — decode, validate the wire schema, drive
//     the core, encode;
//   - middleware.go: the cross-cutting concerns every route composes
//     explicitly in routes();
//   - const.go: every constant the adapter owns, grouped by the concern
//     that reads it.
//
// This package never logs — observability is decorators from internal/logs
// and internal/metrics, spliced in at the seams Deps declares. Two rules
// from the core hold here too: API keys and message content never reach a
// log line, and raw provider errors never reach a caller — the error
// writers below emit only gateway-authored messages.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
)

// errorBody is the one shape of every non-2xx response (see ErrorBody in
// openapi.yaml). Codes come from the core's ErrorCode table plus the three
// only the edge can produce.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code          string        `json:"code"`
	Message       string        `json:"message"`
	CorrelationID string        `json:"correlation_id"`
	Details       *errorDetails `json:"details,omitempty"`
}

type errorDetails struct {
	Param             string     `json:"param,omitempty"`
	RetryAfterSeconds int        `json:"retry_after_seconds,omitempty"`
	Quota             *quotaWire `json:"quota,omitempty"`
	MaxInFlight       int        `json:"max_in_flight,omitempty"`
	RequestedModel    string     `json:"requested_model,omitempty"`
}

type quotaWire struct {
	Limit         int `json:"limit"`
	WindowSeconds int `json:"window_seconds"`
	Used          int `json:"used"`
}

// apiError is a caller-visible failure decided at the edge or translated
// from a *gateway.Error; writeError turns it into the wire ErrorBody.
type apiError struct {
	status  int
	code    string
	message string
	details *errorDetails
}

func unsupportedParameter(param, message string) *apiError {
	return &apiError{
		status:  http.StatusBadRequest,
		code:    codeUnsupportedParameter,
		message: message,
		details: &errorDetails{Param: param},
	}
}

func invalidRequest(message string) *apiError {
	return &apiError{status: http.StatusBadRequest, code: "invalid_request", message: message}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoding a value this package built cannot fail; if the connection is
	// gone the write is a no-op either way.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError emits the ErrorBody contract: the code, a gateway-authored
// message, the request's correlation ID, and Retry-After on every 429.
func writeError(w http.ResponseWriter, r *http.Request, e *apiError) {
	if e.details != nil && e.details.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.details.RetryAfterSeconds))
	}

	writeJSON(w, e.status, errorBody{Error: errorDetail{
		Code:          e.code,
		Message:       e.message,
		CorrelationID: correlationFrom(r.Context()),
		Details:       e.details,
	}})
}

// randomID returns n random bytes hex-encoded — correlation IDs and
// completion IDs.
func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // never fails per crypto/rand's contract
	return hex.EncodeToString(b)
}
