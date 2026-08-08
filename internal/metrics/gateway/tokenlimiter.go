package metrics

import (
	"context"
	"sync/atomic"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// TokenLimiter counts the token budget's decisions and its two distinct
// failures. A failed check is a fail-open — the budget went unenforced for one
// request. A failed debit is different in kind and gets its own counter: the
// request succeeded, and what was lost is the accounting, so folding it into
// the fail-open number would hide an under-charging window behind a metric
// that reads as an outage.
type TokenLimiter struct {
	limiter gateway.TokenLimiter

	allowed  atomic.Int64
	denied   atomic.Int64
	failOpen atomic.Int64

	debited      atomic.Int64
	debitsFailed atomic.Int64
}

// NewTokenLimiter wraps next, counting every decision and every debit.
func NewTokenLimiter(next gateway.TokenLimiter) *TokenLimiter {
	return &TokenLimiter{limiter: next}
}

func (l *TokenLimiter) Check(ctx context.Context, app string) (gateway.RateDecision, error) {
	decision, err := l.limiter.Check(ctx, app)

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

func (l *TokenLimiter) Settle(ctx context.Context, app string, tokens int) error {
	err := l.limiter.Settle(ctx, app, tokens)
	if err != nil {
		l.debitsFailed.Add(1)
		return err
	}
	l.debited.Add(int64(tokens))

	return nil
}

// Allowed reports how many requests had budget left.
func (l *TokenLimiter) Allowed() int64 { return l.allowed.Load() }

// Denied reports how many requests a spent budget refused.
func (l *TokenLimiter) Denied() int64 { return l.denied.Load() }

// FailOpen reports how many requests were admitted unmetered because the
// limiter itself failed.
func (l *TokenLimiter) FailOpen() int64 { return l.failOpen.Load() }

// Debited reports the tokens successfully charged against budgets — the
// gateway's own view of what its windows counted.
func (l *TokenLimiter) Debited() int64 { return l.debited.Load() }

// DebitsFailed reports how many served completions never reached their
// budget. Each one is a window charging less than it should.
func (l *TokenLimiter) DebitsFailed() int64 { return l.debitsFailed.Load() }
