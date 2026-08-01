package metrics

import (
	"context"
	"sync/atomic"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

var _ gateway.RateLimiter = (*RateLimiter)(nil)

// RateLimiter counts allowed and denied decisions, and separately the
// limiter's own outages — a fail-open is neither an allow nor a deny, it is
// the quota going unenforced.
type RateLimiter struct {
	limiter gateway.RateLimiter

	allowed  atomic.Int64
	denied   atomic.Int64
	failOpen atomic.Int64
}

// NewRateLimiter wraps next, counting every decision it makes.
func NewRateLimiter(next gateway.RateLimiter) *RateLimiter {
	return &RateLimiter{limiter: next}
}

func (l *RateLimiter) Allow(ctx context.Context, app string) (gateway.RateDecision, error) {
	decision, err := l.limiter.Allow(ctx, app)

	switch {
	case err != nil:
		l.failOpen.Add(1)
	case decision.Allowed:
		l.allowed.Add(1)
	default:
		l.denied.Add(1)
	}

	return decision, err
}

// Allowed reports how many requests this instance admitted.
func (l *RateLimiter) Allowed() int64 { return l.allowed.Load() }

// Denied reports how many requests a quota refused.
func (l *RateLimiter) Denied() int64 { return l.denied.Load() }

// FailOpen reports how many requests were admitted unmetered because the
// limiter itself failed.
func (l *RateLimiter) FailOpen() int64 { return l.failOpen.Load() }
