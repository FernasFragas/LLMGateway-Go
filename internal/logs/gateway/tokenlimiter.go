package logs

import (
	"context"
	"log/slog"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// TokenLimiter logs the limiter's own failures — both errors the core
// swallows. A failed check means the budget went unenforced for one request;
// a failed debit means a served completion's tokens were never counted, so
// the window under-charges until it rolls. Neither can reach a caller, which
// is exactly why they have to reach the log.
type TokenLimiter struct {
	limiter gateway.TokenLimiter
	log     *slog.Logger
}

// NewTokenLimiter wraps next; a nil log means slog.Default().
func NewTokenLimiter(next gateway.TokenLimiter, log *slog.Logger) *TokenLimiter {
	return &TokenLimiter{limiter: next, log: logs.OrDefault(log)}
}

func (l *TokenLimiter) Check(ctx context.Context, app string) (gateway.RateDecision, error) {
	decision, err := l.limiter.Check(ctx, app)
	if err != nil {
		l.log.ErrorContext(ctx, "token limiter down, budget unenforced", "app", app, "cause", err)
	}

	return decision, err
}

func (l *TokenLimiter) Settle(ctx context.Context, app string, tokens int) error {
	err := l.limiter.Settle(ctx, app, tokens)
	if err != nil {
		l.log.ErrorContext(ctx, "token debit lost, budget under-counted",
			"app", app, "tokens", tokens, "cause", err)
	}

	return err
}
