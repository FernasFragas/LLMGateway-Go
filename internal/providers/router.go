// Package providers is the ProviderClient adapter that turns a route into a
// dialect. The core speaks one port and knows three provider names only as
// data; this package owns the table that maps a name to the adapter fluent in
// it, so adding a fourth provider is a wiring change in main and nothing the
// core or the edge ever sees.
//
// It also closes decision #8's loop. A provider refusing the gateway's own
// credential arrives as gateway.FaultRejected, which is the one fault a
// refresh can fix — so the router reports it to the key cache and returns the
// fault unchanged. The failing request fails over immediately; the reload
// happens off the request path. The router never retries, never logs, and
// never handles: failover is the core's, one attempt only (decision #2).
package providers

import (
	"context"
	"errors"
	"fmt"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// Rejected is told which provider refused the gateway's credential, so a
// stale key cache can schedule its own reload. Implementations must not
// block: they are called on the request path, from a request that has already
// failed.
type Rejected func(provider string)

// Router dispatches one attempt to the adapter that speaks the route's
// provider. The zero value is not usable; call NewRouter.
type Router struct {
	adapters   map[string]gateway.ProviderClient
	onRejected Rejected
}

// NewRouter builds the table, keyed by provider name — `routes[].provider`
// and `failover_order` already use that identity, and a second one would let
// routing and credentials disagree about who a provider is.
//
// onRejected may be nil, which is the honest wiring for a gateway whose
// providers need no credentials.
func NewRouter(adapters map[string]gateway.ProviderClient, onRejected Rejected) (*Router, error) {
	if len(adapters) == 0 {
		return nil, errors.New("providers: at least one adapter is required")
	}
	for name, adapter := range adapters {
		if adapter == nil {
			return nil, fmt.Errorf("providers: adapter for %q is nil", name)
		}
	}

	table := make(map[string]gateway.ProviderClient, len(adapters))
	for name, adapter := range adapters {
		table[name] = adapter
	}

	return &Router{adapters: table, onRejected: onRejected}, nil
}

// Complete hands the attempt to the route's adapter.
//
// A route naming a provider with no adapter is a config mistake that reaches
// the request path — reported as unreachable, the kind that costs nothing
// upstream, so the core fails over to a route that might work instead of
// charging the caller for a wiring error.
func (r *Router) Complete(ctx context.Context, mp gateway.ModelProvider, req gateway.ChatRequest) (gateway.Completion, error) {
	adapter, ok := r.adapters[mp.Provider]
	if !ok {
		return gateway.Completion{}, &gateway.ProviderFault{
			Kind:  gateway.FaultUnreachable,
			Cause: fmt.Errorf("no adapter wired for provider %q", mp.Provider),
		}
	}

	completion, err := adapter.Complete(ctx, mp, req)
	if err != nil && r.onRejected != nil && rejected(err) {
		r.onRejected(mp.Provider)
	}

	return completion, err
}

// rejected reports whether the provider refused the gateway's credential —
// the only fault a key refresh can fix.
func rejected(err error) bool {
	var fault *gateway.ProviderFault

	return errors.As(err, &fault) && fault.Kind == gateway.FaultRejected
}
