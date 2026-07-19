// Package logs adapts the gateway's ports to structured logging, organized
// by the package whose ports are decorated:
//
//   - gateway/: the core's ports (ProviderClient, RateLimiter, AppDirectory,
//     SlotLimiter, UsageRecorder), one file per port;
//   - api/: the edge — the http.Handler seams routes() declares and the
//     ports the api adapter consumes (ChatService, HealthChecker);
//   - auth/: the background key refresh — a failed JWKS fetch keeps serving
//     stale keys (fail static), so its log line is the only signal.
//
// This parent package holds only what both share. Two rules hold in every
// line the subpackages emit: API keys are never logged, and message content
// is never logged — the gateway is content-blind and its logs must be too.
package logs

import "log/slog"

// OrDefault resolves the logger a decorator was built with; nil means
// slog.Default().
func OrDefault(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}

	return log
}
