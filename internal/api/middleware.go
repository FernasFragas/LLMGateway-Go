package api

import (
	"context"
	"net/http"
	"strings"
)

// Middleware here covers only what the edge owns. Rate limiting and API-key
// resolution are deliberately absent: they are core business rules
// (RateLimiter, SlotLimiter, AppDirectory ports), and enforcing them a
// second time at the edge would fork the contract. authenticate extracts
// the credential; the core judges it. Observability is absent too: request
// logging and metering are decorators from internal/logs and
// internal/metrics, spliced in at the seams routes() declares — this
// package never logs.

// correlationFrom returns the request's correlation ID; empty only for a
// request that skipped the correlationID middleware, which routes() never
// produces.
func correlationFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyCorrelationID).(string)

	return id
}

func apiKeyFrom(ctx context.Context) string {
	key, _ := ctx.Value(ctxKeyAPIKey).(string)

	return key
}

// recoverPanic makes a panicking handler cost one request, not the process:
// the caller gets the standard ErrorBody, never the panic value. It answers
// only — observing the panic (log, count) belongs to the PanicMiddleware
// decorators spliced just inside it, each of which re-panics; this outermost
// recover is the one that swallows.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				writeError(w, r, &apiError{
					status:  http.StatusInternalServerError,
					code:    codeInternalError,
					message: "internal error",
				})
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// correlationID adopts the client's X-Correlation-ID or generates one, and
// echoes it on every response, success or error — it is the only thread
// joining the caller's log line to this gateway's.
func (s *Server) correlationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Correlation-ID")
		if id == "" || len(id) > maxCorrelationIDLength {
			id = randomID(16)
		}

		w.Header().Set("X-Correlation-ID", id)

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyCorrelationID, id)))
	})
}

// maxBody caps the request body; a caller over the limit reads a 413 from
// the decoder (payload_too_large), not a dropped connection.
func (s *Server) maxBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)

		next.ServeHTTP(w, r)
	})
}

// authenticate extracts the bearer credential and refuses requests that
// carry none — the one auth judgment the edge can make. Whether the key is
// real belongs to the core's AppDirectory; the key itself goes into the
// context and nowhere else.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := bearerToken(r)
		if !ok {
			writeError(w, r, &apiError{
				status:  http.StatusUnauthorized,
				code:    "unauthorized",
				message: "missing or invalid API key",
			})

			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyAPIKey, key)))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if len(auth) > len(bearerPrefix) && strings.EqualFold(auth[:len(bearerPrefix)], bearerPrefix) {
		return auth[len(bearerPrefix):], true
	}

	return "", false
}
