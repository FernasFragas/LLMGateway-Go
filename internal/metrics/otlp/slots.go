package otlp

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/FernasFragas/LLMGateway-Go/internal/slots"
)

// RegisterSlots attaches the in_flight gauge directly to the real slot
// limiter, not its metrics decorator: in_flight is instantaneous occupancy
// (how many slots an app holds right now), which the decorator's
// cumulative Acquired/Refused counters cannot express — InFlight is already
// the exact point-in-time read this gauge needs. apps lists every
// configured app name so one that currently holds zero slots still reports
// zero instead of going missing from the series.
func RegisterSlots(meter metric.Meter, limiter *slots.Limiter, apps []string) error {
	_, err := meter.Int64ObservableGauge("in_flight",
		metric.WithDescription("in-flight request slots currently held, per app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range apps {
				o.Observe(int64(limiter.InFlight(app)), metric.WithAttributes(attribute.String("app", app)))
			}
			return nil
		}),
	)

	return err
}
