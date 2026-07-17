package api

// Every constant this adapter owns, grouped by the concern that reads it —
// the values of the wire contract and the transport's tuning, on one screen.

import "time"

// Transport defaults; zero-valued Config fields take these (see New).
const (
	defaultAddr              = ":8080"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 90 * time.Second
	defaultIdleTimeout       = 2 * time.Minute
	defaultMaxBodyBytes      = 1 << 20 // 1 MiB — prompts, not uploads
)

// The failures only the edge can see; the core's codes cover the rest.
const (
	codeUnsupportedParameter = "unsupported_parameter" // 400
	codePayloadTooLarge      = "payload_too_large"     // 413
	codeInternalError        = "internal_error"        // 500 — a recovered panic
)

// statusClientClosedRequest is nginx's convention for a caller that vanished
// mid-request. Nothing is written for anyone to read — it exists so the
// request log records "caller left" instead of a lying 5xx.
const statusClientClosedRequest = 499

// maxCorrelationIDLength caps a client-supplied ID (see CorrelationId in
// openapi.yaml); longer ones are replaced, not truncated — a partial ID
// correlates with nothing.
const maxCorrelationIDLength = 128

// bearerPrefix is the Authorization scheme the edge accepts, matched
// case-insensitively per RFC 9110.
const bearerPrefix = "Bearer "

// ctxKey keys the request-scoped facts middleware deposits for handlers.
type ctxKey int

const (
	ctxKeyCorrelationID ctxKey = iota
	ctxKeyAPIKey
)
