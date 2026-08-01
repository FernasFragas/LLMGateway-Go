package metrics

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

var _ gateway.ProviderClient = (*ProviderClient)(nil)

// ProviderClient counts provider attempts and their failures by fault kind.
// Kind is the only label — fixed cardinality regardless of how many
// providers or models the router dispatches to.
type ProviderClient struct {
	provider gateway.ProviderClient

	attempts atomic.Int64
	failures atomic.Int64

	mu     sync.Mutex
	byKind map[gateway.FaultKind]int64
}

// NewProviderClient wraps next, counting every attempt it serves.
func NewProviderClient(next gateway.ProviderClient) *ProviderClient {
	return &ProviderClient{provider: next, byKind: make(map[gateway.FaultKind]int64)}
}

func (c *ProviderClient) Complete(ctx context.Context, mp gateway.ModelProvider, req gateway.ChatRequest) (gateway.Completion, error) {
	completion, err := c.provider.Complete(ctx, mp, req)

	c.attempts.Add(1)
	if err != nil {
		c.failures.Add(1)

		kind := gateway.FaultKind("unclassified")
		var fault *gateway.ProviderFault
		if errors.As(err, &fault) {
			kind = fault.Kind
		}

		c.mu.Lock()
		c.byKind[kind]++
		c.mu.Unlock()
	}

	return completion, err
}

// Attempts reports every attempt this instance observed, served or failed.
func (c *ProviderClient) Attempts() int64 { return c.attempts.Load() }

// Failures reports how many of them failed.
func (c *ProviderClient) Failures() int64 { return c.failures.Load() }

// FailuresByKind reports how many failures classified as kind.
func (c *ProviderClient) FailuresByKind(kind gateway.FaultKind) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.byKind[kind]
}
