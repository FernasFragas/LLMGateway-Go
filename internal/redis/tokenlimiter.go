package redis

import (
	"context"
	"strconv"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// tokenWindow is the metering period for the token-rate currency. A minute
// keeps the counter's meaning literal — "tokens per minute" is the budget
// operators configure, and it is the unit the providers themselves cap.
const tokenWindow = time.Minute

// spentScript reads what an app has already settled this window. It is a
// script rather than a plain GET because GET answers with a bulk string and
// this module's client reads integer replies only; returning through Lua
// converts the reply and folds the never-written case into a 0.
const spentScript = `local v = redis.call('GET', KEYS[1])
if not v then return 0 end
return tonumber(v)`

// debitScript adds a completion's cost and gives the key a lifetime the first
// time it appears. Same reasoning as the request-rate limiter's script: INCRBY
// and EXPIRE as separate round trips can be interrupted between them, and a
// budget key left without a TTL would refuse an app forever. The first write
// is the one whose result equals its own increment — every later one is
// strictly larger, because a zero debit never reaches Redis.
const debitScript = `local n = redis.call('INCRBY', KEYS[1], ARGV[1])
if n == tonumber(ARGV[1]) then redis.call('EXPIRE', KEYS[1], ARGV[2]) end
return n`

// TokenLimiter meters the token-rate currency per app, in counters every
// replica shares — an app's 200k tokens per minute is one budget across the
// deployment, not one per pod.
//
// It is a soft ceiling by construction (ADR-003). Check judges spend that has
// already settled, so requests in flight are invisible to it: an app one token
// under budget is admitted, and everything already running settles afterward.
// What bounds the overshoot is the app's slot ceiling, which is why the two
// limits are set together rather than independently.
//
// Like the request-rate limiter it never fails closed. A store error travels
// to the core, which admits the request unmetered and records the degradation
// (decision #1).
type TokenLimiter struct {
	client  *Client
	budgets map[string]int   // tokens per window, by app; 0 or absent: unmetered
	now     func() time.Time // swapped in tests
}

// NewTokenLimiter builds the limiter over per-app token budgets. A zero budget
// and an app with no entry both mean unmetered, the same convention the
// request-rate limiter, the slot limiter, and global_max_in_flight use — an
// app pays for what it configures, not for what it omits.
func NewTokenLimiter(client *Client, budgets map[string]int) *TokenLimiter {
	own := make(map[string]int, len(budgets))
	for app, budget := range budgets {
		own[app] = budget
	}

	return &TokenLimiter{client: client, budgets: own, now: time.Now}
}

// Check reports whether app has budget left in the current window.
//
// The comparison is "at or above", not "above": because the debit lands after
// the response, the only question this can honestly ask is whether the app has
// already spent its minute. A refusal carries the budget, the spend, and how
// long until the window resets, so the caller is told what to do rather than
// left to guess.
func (l *TokenLimiter) Check(ctx context.Context, app string) (gateway.RateDecision, error) {
	budget := l.budgets[app]
	if budget <= 0 {
		return gateway.RateDecision{Allowed: true}, nil
	}

	now := l.now()
	spent, err := l.client.Int(ctx, "EVAL", spentScript, "1", l.key(app, now))
	if err != nil {
		return gateway.RateDecision{}, err
	}

	if int(spent) < budget {
		return gateway.RateDecision{Allowed: true}, nil
	}

	return gateway.RateDecision{
		Allowed:    false,
		RetryAfter: tokenWindow - time.Duration(now.UnixNano()%int64(tokenWindow)),
		Quota: &gateway.QuotaDetail{
			Limit:         budget,
			WindowSeconds: int(tokenWindow / time.Second),
			Used:          int(spent),
		},
	}, nil
}

// Settle debits what a served completion cost.
//
// An unmetered app and a zero-token completion both cost no round trip: there
// is no budget to move them against. The debit is unconditional otherwise —
// it never refuses, because by the time it runs the tokens are already spent
// and the only question left is whether the counter records them.
func (l *TokenLimiter) Settle(ctx context.Context, app string, tokens int) error {
	if l.budgets[app] <= 0 || tokens <= 0 {
		return nil
	}

	ttl := strconv.Itoa(int(tokenWindow/time.Second) + 1) // outlive the window, never the next one
	_, err := l.client.Int(ctx, "EVAL", debitScript, "1", l.key(app, l.now()), strconv.Itoa(tokens), ttl)

	return err
}

// key names the app's counter for the window containing now. Both methods
// derive it the same way, so a debit lands in the window its own check read.
func (l *TokenLimiter) key(app string, now time.Time) string {
	return "llmgw:tpm:" + app + ":" + strconv.FormatInt(now.UnixNano()/int64(tokenWindow), 10)
}
