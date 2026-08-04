package otlp

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	keysmetrics "github.com/FernasFragas/LLMGateway-Go/internal/metrics/providerkeys"
	"github.com/FernasFragas/LLMGateway-Go/internal/providerkeys"
)

// RegisterProviderKeys attaches the refresh counters from the metrics
// decorator, the out-of-band trigger counter, and the staleness gauge the
// cache itself exposes via Age. providers lists which provider names to
// report — main already has this list (cache.Providers()) at construction
// time.
//
// The gauge is what decision #1 depends on: fail static is only defensible
// because the staleness is visible. Age's callback runs at each collection
// cycle rather than on a background sampler, matching how the cache already
// treats Age as a point-in-time read.
func RegisterProviderKeys(meter metric.Meter, refresher *keysmetrics.Refresher, triggers *keysmetrics.TriggerCounter, cache *providerkeys.Cache, providers []string) error {
	if _, err := meter.Int64ObservableCounter("provider_key_refresh_total",
		metric.WithDescription("provider credential refreshes attempted, scheduled or triggered"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(refresher.Refreshes())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("provider_key_refresh_failures_total",
		metric.WithDescription("provider credential refreshes that failed — a rotation healing itself looks nothing like a source outage"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(refresher.Failures())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("provider_key_refresh_triggered_total",
		metric.WithDescription("out-of-band refreshes actually started after a provider rejected the cached credential"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, provider := range providers {
				if n := triggers.Triggered(provider); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("provider", provider)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Float64ObservableGauge("provider_key_age_seconds",
		metric.WithDescription("time since each provider's credential last loaded successfully; absent until the first load"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			for _, provider := range providers {
				age, ok := cache.Age(provider)
				if !ok {
					continue
				}
				o.Observe(age.Seconds(), metric.WithAttributes(attribute.String("provider", provider)))
			}
			return nil
		}),
	); err != nil {
		return err
	}

	return nil
}
