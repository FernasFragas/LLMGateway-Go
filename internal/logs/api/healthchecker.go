package logs

import (
	"context"
	"log/slog"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"

	"github.com/FernasFragas/LLMGateway-Go/internal/api"
)

// HealthLogChecker logs why readiness was refused. The probe body stays terse
// by decision, so this line is where an operator learns why a pod takes no
// traffic — for that fact the log is the only outlet.
type HealthLogChecker struct {
	healthChecker api.HealthChecker
	log           *slog.Logger
}

// NewHealthChecker wraps next; a nil log means slog.Default().
func NewHealthChecker(next api.HealthChecker, log *slog.Logger) *HealthLogChecker {
	return &HealthLogChecker{healthChecker: next, log: logs.OrDefault(log)}
}

func (c *HealthLogChecker) Live() error { return c.healthChecker.Live() }

func (c *HealthLogChecker) Ready(ctx context.Context) error {
	err := c.healthChecker.Ready(ctx)
	if err != nil {
		c.log.WarnContext(ctx, "readiness refused", "reason", err.Error())
	}

	return err
}
