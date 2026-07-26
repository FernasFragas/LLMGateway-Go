package metrics

import (
	"net/http"
	"sync/atomic"
)

// RequestMetrics counts served requests and how many were answered 5xx. One
// instance meters every route it wraps — Wrap is the middleware, the
// counters are shared.
type RequestMetrics struct {
	requests     atomic.Int64
	serverErrors atomic.Int64
}

func NewRequestMetrics() *RequestMetrics {
	return &RequestMetrics{}
}

// Wrap meters next. Splice it outside the panic-answering middleware so a
// recovered panic's 500 counts like any other response.
func (m *RequestMetrics) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		m.requests.Add(1)
		if rec.status >= http.StatusInternalServerError {
			m.serverErrors.Add(1)
		}
	})
}

// Requests reports how many requests completed through this instance.
func (m *RequestMetrics) Requests() int64 { return m.requests.Load() }

// ServerErrors reports how many of them were answered 5xx.
func (m *RequestMetrics) ServerErrors() int64 { return m.serverErrors.Load() }

// statusRecorder captures the status code for the counters. Each observing
// package owns its copy — the price of adapters that share no types.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code

	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush and deadlines through the
// recorder — without it, wrapping would silently break streaming.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
