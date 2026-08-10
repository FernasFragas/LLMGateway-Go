package metrics

import (
	"slices"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// SlotLimiter counts granted and refused in-flight slots — the third
// currency's own volume, distinct from the rate limiter's.
//
// Both carry the app, because a ceiling is per app and a process-wide refusal
// count cannot answer the only question worth asking of it: which caller is
// saturated. The app set is closed at construction, the same bound every
// counter here follows (see doc.go).
type SlotLimiter struct {
	slot gateway.SlotLimiter

	apps []string

	acquired *keyedCounter[string]
	refused  *keyedCounter[string]
}

// NewSlotLimiter wraps next, counting every claim it decides. apps is the
// closed set of configured app names.
func NewSlotLimiter(next gateway.SlotLimiter, apps []string) *SlotLimiter {
	own := slices.Clone(apps)
	slices.Sort(own)

	return &SlotLimiter{
		slot:     next,
		apps:     own,
		acquired: newKeyedCounter(own),
		refused:  newKeyedCounter(own),
	}
}

// Apps reports the configured app names, sorted.
func (l *SlotLimiter) Apps() []string { return slices.Clone(l.apps) }

func (l *SlotLimiter) TryAcquire(app string) (release func(), ceiling int, ok bool) {
	release, ceiling, ok = l.slot.TryAcquire(app)

	if ok {
		l.acquired.add(app, 1)
	} else {
		l.refused.add(app, 1)
	}

	return release, ceiling, ok
}

// Acquired reports how many slots this instance granted.
func (l *SlotLimiter) Acquired() int64 { return l.acquired.total() }

// AcquiredByApp reports how many of them were app's.
func (l *SlotLimiter) AcquiredByApp(app string) int64 { return l.acquired.get(app) }

// Refused reports how many claims a ceiling refused.
func (l *SlotLimiter) Refused() int64 { return l.refused.total() }

// RefusedByApp reports how many of app's claims its ceiling refused — the
// starvation signal, and the series the brief's slot-ceiling criterion names.
func (l *SlotLimiter) RefusedByApp(app string) int64 { return l.refused.get(app) }
