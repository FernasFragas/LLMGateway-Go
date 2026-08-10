package otlp

import (
	"context"

	"go.opentelemetry.io/otel/metric"

	"github.com/FernasFragas/LLMGateway-Go/internal/auth"
)

// RegisterAuth attaches the inbound-identity instruments: today, the one
// gauge that makes the JWKS cache's fail-static behavior observable.
//
// It reads the cache directly rather than through a decorator because there
// is nothing to decorate — staleness is not an event on a seam, it is a
// property of the cache read at each collection cycle. The provider-key
// staleness gauge is built the same way.
func RegisterAuth(meter metric.Meter, keys *auth.JWKSCache) error {
	// The counterpart to provider_key_age_seconds, and the more urgent of the
	// two: a stale provider key degrades one provider and failover carries
	// the load, while stale signing keys stay invisible until the cluster
	// rotates and then reject every caller at once.
	//
	// No data point at all until the first successful load. The alternative
	// is not a zero but a ~9.2e9-second age measured from the zero time,
	// which would page someone about a pod that is simply starting — and
	// readiness already keeps traffic off that pod.
	_, err := meter.Float64ObservableGauge("jwks_cache_age_seconds",
		metric.WithDescription("time since the cluster's signing keys last loaded successfully; absent until the first load"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			if age, loaded := keys.Age(); loaded {
				o.Observe(age.Seconds())
			}
			return nil
		}),
	)

	return err
}
