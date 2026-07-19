package logs

import (
	"log/slog"

	"net/http"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"
)

var _ http.Handler = (*RequestLogger)(nil)

// RequestLogger emits one line per request: method, path, status, duration,
// correlation ID. Never the body, never the Authorization header — the
// gateway is content-blind and its request log must be too.
//
// It reads the correlation ID from the response header the edge echoes, so
// the adapters stay independent. Wire it inside the middleware that sets
// X-Correlation-ID and outside the one that answers panics — that way every
// response, a recovered panic's 500 included, gets its line.
type RequestLogger struct {
	next http.Handler
	log  *slog.Logger
}

// NewRequestLogger wraps next; a nil log means slog.Default().
func NewRequestLogger(next http.Handler, log *slog.Logger) *RequestLogger {
	return &RequestLogger{next: next, log: logs.OrDefault(log)}
}

func (l *RequestLogger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	l.next.ServeHTTP(rec, r)

	l.log.InfoContext(r.Context(), "request served",
		"method", r.Method,
		"path", r.URL.Path,
		"status", rec.status,
		"duration_ms", time.Since(start).Milliseconds(),
		"correlation_id", w.Header().Get("X-Correlation-ID"),
	)
}

// statusRecorder captures the status code for the request log.
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
