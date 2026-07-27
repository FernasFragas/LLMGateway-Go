package redis

import (
	"context"
	"strconv"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// window is the metering period for the request-rate currency. One second
// keeps the counter's meaning literal — "requests per second" is the quota
// operators configure — and keeps each key's lifetime short enough that
// expiry, not eviction, reclaims it.
const window = time.Second

// incrementScript counts one request and gives the key a lifetime the first
// time it appears. It runs as one script because INCR and EXPIRE as separate
// round trips can be interrupted between them: a crash in that gap leaves a
// key with no TTL, and an app that hit its quota once would stay refused
// forever. Redis runs a script atomically, which closes it.
const incrementScript = `local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
return n`

// Limiter meters the request-rate currency per app, in counters every replica
// shares — the point of putting them in Redis rather than in each process.
//
// It never fails closed. When the store is unreachable the error travels to
// the core, which admits the request unmetered and records the degradation
// (decision #1): a brief unenforced window beats an outage, and quotas are
// the one thing here that may safely go unenforced.
type Limiter struct {
	client *Client
	limits map[string]int   // requests per window, by app; 0 or absent: unmetered
	now    func() time.Time // swapped in tests
}

// NewLimiter builds the limiter over per-app request quotas. A zero limit and
// an app with no entry both mean unmetered, the same convention the slot
// limiter and global_max_in_flight use — an app pays for what it configures,
// not for what it omits.
func NewLimiter(client *Client, limits map[string]int) *Limiter {
	own := make(map[string]int, len(limits))
	for app, limit := range limits {
		own[app] = limit
	}

	return &Limiter{client: client, limits: own, now: time.Now}
}

// Allow counts this request against app's quota for the current window.
//
// The count is incremented before the verdict, deliberately: a request that
// is about to be refused still happened, and pricing it is what makes a
// caller's backoff visible instead of free. The refusal carries the window
// that refused and how long until it resets, so the caller is told what to do
// rather than left to guess.
func (l *Limiter) Allow(ctx context.Context, app string) (gateway.RateDecision, error) {
	limit := l.limits[app]
	if limit <= 0 {
		return gateway.RateDecision{Allowed: true}, nil
	}

	now := l.now()
	key := "llmgw:rps:" + app + ":" + strconv.FormatInt(now.UnixNano()/int64(window), 10)
	ttl := strconv.Itoa(int(window/time.Second) + 1) // outlive the window, never the next one

	used, err := l.client.Int(ctx, "EVAL", incrementScript, "1", key, ttl)
	if err != nil {
		return gateway.RateDecision{}, err
	}

	if int(used) <= limit {
		return gateway.RateDecision{Allowed: true}, nil
	}

	return gateway.RateDecision{
		Allowed:    false,
		RetryAfter: window - time.Duration(now.UnixNano()%int64(window)),
		Quota: &gateway.QuotaDetail{
			Limit:         limit,
			WindowSeconds: int(window / time.Second),
			Used:          int(used),
		},
	}, nil
}
