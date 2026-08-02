package metrics

import (
	"sync/atomic"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// SlotLimiter counts granted and refused in-flight slots — the third
// currency's own volume, distinct from the rate limiter's.
type SlotLimiter struct {
	slot gateway.SlotLimiter

	acquired atomic.Int64
	refused  atomic.Int64
}

// NewSlotLimiter wraps next, counting every claim it decides.
func NewSlotLimiter(next gateway.SlotLimiter) *SlotLimiter {
	return &SlotLimiter{slot: next}
}

func (l *SlotLimiter) TryAcquire(app string) (release func(), ceiling int, ok bool) {
	release, ceiling, ok = l.slot.TryAcquire(app)

	if ok {
		l.acquired.Add(1)
	} else {
		l.refused.Add(1)
	}

	return release, ceiling, ok
}

// Acquired reports how many slots this instance granted.
func (l *SlotLimiter) Acquired() int64 { return l.acquired.Load() }

// Refused reports how many claims a ceiling refused.
func (l *SlotLimiter) Refused() int64 { return l.refused.Load() }
