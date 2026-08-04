package otlp

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// meterName identifies this gateway's instruments to the collector — one
// name for every metric this process emits, since there is only one
// process-wide meter.
const meterName = "llm-gateway"

// Provider owns the OTel SDK's push pipeline: the OTLP/gRPC exporter and the
// MeterProvider that periodically reads every registered instrument and
// sends it to endpoint. Shutdown flushes the final export and must run
// before the process exits, or the last collection interval's data is lost.
type Provider struct {
	mp *sdkmetric.MeterProvider
}

// NewProvider dials endpoint (host:port, e.g. otel-collector...:4317) and
// starts the periodic reader. The connection to the collector is
// established lazily by the exporter itself — a collector that is down at
// startup does not fail the boot; it fails individual export cycles, which
// the SDK retries and then drops (never blocking a request).
func NewProvider(ctx context.Context, endpoint string) (*Provider, error) {
	exporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(), // in-cluster traffic to the collector; no public exposure (see non-goals)
	)
	if err != nil {
		return nil, fmt.Errorf("metrics/otlp: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)))

	return &Provider{mp: mp}, nil
}

// Meter returns the meter every Register* function attaches instruments to.
func (p *Provider) Meter() metric.Meter {
	return p.mp.Meter(meterName)
}

// Shutdown flushes pending exports and releases the exporter's connection.
func (p *Provider) Shutdown(ctx context.Context) error {
	return p.mp.Shutdown(ctx)
}
