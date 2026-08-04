// Package otlp is the metrics exporter ADR-002 decided on: every metric
// leaves the gateway via OTLP push through the OpenTelemetry SDK, the same
// pipeline traces and logs already use, and there is no /metrics scrape
// endpoint. The counters it reads (internal/metrics/gateway,
// internal/metrics/providerkeys, internal/metrics/api) stay plain atomics —
// this package only adds observable (asynchronous) instruments whose
// callbacks read those counters' existing accessor methods at each
// collection cycle. Provider owns the SDK's MeterProvider and its periodic
// export; the Register* functions attach one port's counters to it. Nothing
// here is on the request path: a callback is read-only and a failed export
// is retried and then dropped by the SDK, never surfaced to a caller.
package otlp
