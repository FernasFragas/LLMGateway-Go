package logs

import (
	"log/slog"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// SlotLimiter logs refusals with the ceiling that refused — the rejection
// counter carries only the outcome code; the number is visible here.
type SlotLimiter struct {
	slot gateway.SlotLimiter
	log  *slog.Logger
}

// NewSlotLimiter wraps next; a nil log means slog.Default().
func NewSlotLimiter(next gateway.SlotLimiter, log *slog.Logger) *SlotLimiter {
	return &SlotLimiter{slot: next, log: logs.OrDefault(log)}
}

func (l *SlotLimiter) TryAcquire(app string) (release func(), ceiling int, ok bool) {
	release, ceiling, ok = l.slot.TryAcquire(app)
	if !ok {
		l.log.Warn("in-flight ceiling refused a slot", "app", app, "ceiling", ceiling)
	}
	return release, ceiling, ok
}
