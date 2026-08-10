package metrics

import (
	"context"
	"errors"
	"slices"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// ProviderClient counts provider attempts and their failures, both split by
// which provider served them and — for failures — by fault kind.
//
// Provider is the dimension an incident is actually read through: "upstream
// failures are up" is not actionable, "anthropic's timeouts are up" is. Both
// dimensions are closed sets known at boot, so the series count is fixed
// before the first request (see doc.go).
type ProviderClient struct {
	provider gateway.ProviderClient

	providers []string

	attempts *keyedCounter[string]
	failures *keyedCounter[providerKind]
}

// NewProviderClient wraps next, counting every attempt it serves. providers
// is the closed set of configured provider names.
func NewProviderClient(next gateway.ProviderClient, providers []string) *ProviderClient {
	own := slices.Clone(providers)
	slices.Sort(own)

	return &ProviderClient{
		provider:  next,
		providers: own,
		attempts:  newKeyedCounter(own),
		failures:  newKeyedCounter(providerKindKeys(own)),
	}
}

// Providers reports the configured provider names, sorted.
func (c *ProviderClient) Providers() []string { return slices.Clone(c.providers) }

func (c *ProviderClient) Complete(ctx context.Context, mp gateway.ModelProvider, req gateway.ChatRequest) (gateway.Completion, error) {
	completion, err := c.provider.Complete(ctx, mp, req)

	c.attempts.add(mp.Provider, 1)
	if err != nil {
		c.failures.add(providerKind{provider: mp.Provider, kind: faultKind(err)}, 1)
	}

	return completion, err
}

// faultKind classifies a provider error in the core's terms. Anything that
// is not a *ProviderFault lands in the unclassified bucket rather than being
// dropped — the adapter that produced it is the bug, and hiding it would
// make that adapter look healthy.
func faultKind(err error) gateway.FaultKind {
	var fault *gateway.ProviderFault
	if errors.As(err, &fault) {
		return fault.Kind
	}

	return KindUnclassified
}

// Attempts reports every attempt this instance observed, served or failed.
func (c *ProviderClient) Attempts() int64 { return c.attempts.total() }

// AttemptsByProvider reports how many attempts went to one provider.
func (c *ProviderClient) AttemptsByProvider(provider string) int64 {
	return c.attempts.get(provider)
}

// Failures reports how many of them failed.
func (c *ProviderClient) Failures() int64 { return c.failures.total() }

// FailuresBy reports how many of provider's attempts failed as kind.
func (c *ProviderClient) FailuresBy(provider string, kind gateway.FaultKind) int64 {
	return c.failures.get(providerKind{provider: provider, kind: kind})
}
