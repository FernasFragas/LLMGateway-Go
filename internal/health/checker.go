// Package health answers the two questions Kubernetes asks, separately,
// because they trigger different remedies: liveness ("is the process
// healthy?") failing restarts the pod; readiness ("may this pod receive
// traffic?") failing routes around it. The gateway fails closed at
// readiness — the one place refusing harms no traffic (decision #1): a cold
// instance with no key cache says "not ready" instead of serving 401s.
package health

import (
	"context"
	"fmt"
	"sync"
)

// Check reports one readiness condition; nil means the condition holds.
type Check func(ctx context.Context) error

// Checker aggregates the conditions a pod must satisfy before receiving
// traffic. The zero value is not usable; call NewChecker.
type Checker struct {
	mu        sync.RWMutex
	readiness map[string]Check
}

func NewChecker() *Checker {
	return &Checker{readiness: make(map[string]Check)}
}

// AddReadiness registers a named condition that must hold for Ready to
// pass — e.g. "key-cache" reporting whether any API keys are loaded.
func (c *Checker) AddReadiness(name string, check Check) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readiness[name] = check
}

// Live reports process liveness. It is deliberately unconditional: a
// dependency being down is a readiness fact, never a reason to restart a
// healthy process.
func (c *Checker) Live() error { return nil }

// Ready runs every registered condition and reports the first that fails,
// named — the probe log is where an operator learns why a pod takes no
// traffic.
func (c *Checker) Ready(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for name, check := range c.readiness {
		if err := check(ctx); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	return nil
}
