package logs

import (
	"context"
	"log/slog"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

var _ gateway.RateLimiter = (*RateLimiter)(nil)

// RateLimiter logs the limiter's own failure — the error the core swallows
// when it fails open. The unmetered-request count lives in UsageRecorder;
// the cause lives here.
type RateLimiter struct {
	limiter gateway.RateLimiter
	log     *slog.Logger
}

// NewRateLimiter wraps next; a nil log means slog.Default().
func NewRateLimiter(next gateway.RateLimiter, log *slog.Logger) *RateLimiter {
	return &RateLimiter{limiter: next, log: logs.OrDefault(log)}
}

func (l *RateLimiter) Allow(ctx context.Context, app string) (gateway.RateDecision, error) {
	decision, err := l.limiter.Allow(ctx, app)
	if err != nil {
		l.log.ErrorContext(ctx, "rate limiter down, quota unenforced", "app", app, "cause", err)
	}
	return decision, err
}
