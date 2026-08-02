package metrics

import (
	"context"
	"sync/atomic"

	providerkeys "github.com/FernasFragas/LLMGateway-Go/internal/providerkeys"
)

// Refresher counts credential refreshes and their failures. Fail static is
// only defensible while the failing is visible: a flat refresh count with a
// running gateway means the loop died, and failures rising against a steady
// count is a source outage the stale-but-usable key is papering over.
type Refresher struct {
	next providerkeys.Refresher

	refreshes atomic.Int64
	failures  atomic.Int64
}

// NewRefresher wraps next, counting every refresh it serves.
func NewRefresher(next providerkeys.Refresher) *Refresher {
	return &Refresher{next: next}
}

func (r *Refresher) Refresh(ctx context.Context, provider string) error {
	err := r.next.Refresh(ctx, provider)

	r.refreshes.Add(1)
	if err != nil {
		r.failures.Add(1)
	}

	return err
}

// Refreshes reports every attempt this instance observed, served or failed.
func (r *Refresher) Refreshes() int64 { return r.refreshes.Load() }

// Failures reports how many of them failed.
func (r *Refresher) Failures() int64 { return r.failures.Load() }
