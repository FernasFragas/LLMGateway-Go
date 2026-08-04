package otlp

import (
	"context"

	"go.opentelemetry.io/otel/metric"

	apimetrics "github.com/FernasFragas/LLMGateway-Go/internal/metrics/api"
)

// RegisterAPI attaches observable instruments over the HTTP edge's request
// and panic counters — the same instances main already hands to newServer.
func RegisterAPI(meter metric.Meter, reqs *apimetrics.RequestMetrics, panics *apimetrics.PanicCounter) error {
	if _, err := meter.Int64ObservableCounter("http_requests_total",
		metric.WithDescription("requests the edge finished serving"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(reqs.Requests())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("http_server_errors_total",
		metric.WithDescription("requests answered 5xx, panics included"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(reqs.ServerErrors())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("http_panics_total",
		metric.WithDescription("handler panics recovered at the edge"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(panics.Panics())
			return nil
		}),
	); err != nil {
		return err
	}

	return nil
}
