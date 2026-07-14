package gateway

import (
	"context"
	"errors"
	"time"
)

// ErrorCode is the machine-readable outcome of a refused or failed request.
// The values map 1:1 to the failure-modes table in the architecture brief
// and to the ErrorBody codes in the OpenAPI spec.
type ErrorCode string

const (
	CodeInvalidRequest     ErrorCode = "invalid_request"     // 400
	CodeUnauthorized       ErrorCode = "unauthorized"        // 401
	CodeQuotaExceeded      ErrorCode = "quota_exceeded"      // 429 — rate/token window
	CodeConcurrencyCeiling ErrorCode = "concurrency_ceiling" // 429 — in-flight slots
	CodeUpstreamFailed     ErrorCode = "upstream_failed"     // 502
	CodeModelUnavailable   ErrorCode = "model_unavailable"   // 503
	CodeGatewayTimeout     ErrorCode = "gateway_timeout"     // 504
)

// Error is the caller-visible failure of one request. Message is always
// written by the gateway — raw provider errors never leak (decision #4).
// The typed fields carry the code-specific details the API exposes.
type Error struct {
	Code    ErrorCode
	Message string

	RetryAfter     time.Duration // quota_exceeded, concurrency_ceiling: when to try again
	Quota          *QuotaDetail  // quota_exceeded: the window that refused
	MaxInFlight    int           // concurrency_ceiling: the app's slot ceiling
	RequestedModel string        // model_unavailable: the model with no eligible model provider
}

func (e *Error) Error() string {
	return string(e.Code) + ": " + e.Message
}

// QuotaDetail describes the rate window that refused a request.
type QuotaDetail struct {
	Limit         int
	WindowSeconds int
	Used          int
}

// FaultKind classifies a provider-scoped failure. Every kind triggers the
// same recovery — at most one failover, per the app's policy — but they
// differ in what they cost: a timeout or a garbage 200 may already be billed
// upstream (decision #6).
type FaultKind string

const (
	FaultUnreachable FaultKind = "unreachable"  // DNS / TLS / connection failure: zero bytes sent
	FaultTimeout     FaultKind = "timeout"      // no response before the per-try deadline
	FaultServerError FaultKind = "server_error" // response status >= 500
	FaultThrottled   FaultKind = "throttled"    // provider-side 429 — never presented as the caller's fault
	FaultBadResponse FaultKind = "bad_response" // 200 but malformed, truncated, or schema-mismatched
)

// ProviderFault is how a provider adapter reports an upstream failure in the
// core's terms. Cause is for logs only; it never reaches a caller.
type ProviderFault struct {
	Kind FaultKind
	// ObservedUsage carries whatever token accounting could still be parsed
	// from the failed exchange; nil when nothing was observable. When set it
	// tightens the unobserved-spend estimate from max_tokens down to what
	// the provider actually reported (decision #6).
	ObservedUsage *Usage
	Cause         error
}

func (f *ProviderFault) Error() string {
	if f.Cause != nil {
		return "provider fault (" + string(f.Kind) + "): " + f.Cause.Error()
	}
	return "provider fault (" + string(f.Kind) + ")"
}

func (f *ProviderFault) Unwrap() error { return f.Cause }

// billsDespiteFailure reports whether the provider may charge for tokens the
// gateway never received: timeouts (the upstream may still complete) and
// garbage 200s (the upstream already billed for the garbage). Failing over
// anyway knowingly risks paying twice — decision #6.
func billsDespiteFailure(err error) bool {
	var fault *ProviderFault
	if errors.As(err, &fault) {
		return fault.Kind == FaultTimeout || fault.Kind == FaultBadResponse
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// unobservedTokens is the upper-bound spend estimate for an attempt whose
// billing the gateway cannot observe: the provider-reported usage when any
// was parsed, otherwise the request's own cost cap.
func unobservedTokens(err error, req ChatRequest) int {
	var fault *ProviderFault
	if errors.As(err, &fault) && fault.ObservedUsage != nil {
		return fault.ObservedUsage.TotalTokens
	}
	return req.MaxTokens
}
