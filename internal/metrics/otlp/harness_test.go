package otlp

// Test harness: a manual reader stands in for the OTLP push pipeline, so
// these tests assert what a collection cycle would observe without a real
// collector. Builders hide mechanics, never meaning — each test states one
// clause of the registration contract: the callback reads the counter's
// current value.

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	gwmetrics "github.com/FernasFragas/LLMGateway-Go/internal/metrics/gateway"
)

// testApps and testProviders are the closed sets these tests configure the
// decorators with — the dimensions the callbacks enumerate.
var (
	testApps      = []string{"rag-api", "agent-service"}
	testProviders = []string{"openai", "anthropic"}
)

// registerUsageOnly registers the full gateway instrument set with idle
// decorators behind every port but the usage recorder, so a test about usage
// states only the usage it recorded — the other counters observe nothing and
// stay out of the assertions.
func registerUsageOnly(t *testing.T, meter metric.Meter, usage *gwmetrics.UsageRecorder) {
	t.Helper()

	err := RegisterGateway(meter,
		gwmetrics.NewAppDirectory(staticApps{}),
		gwmetrics.NewRateLimiter(limiter{}),
		gwmetrics.NewTokenLimiter(tokenStub{}),
		gwmetrics.NewSlotLimiter(slotStub{}, testApps),
		gwmetrics.NewProviderClient(provider{}, testProviders),
		usage,
	)
	if err != nil {
		t.Fatalf("RegisterGateway: %v", err)
	}
}

// collectedByAttr reads one instrument's data points keyed by a single string
// attribute. collected() flattens attributes away, which is enough to prove a
// counter moved but not that a label actually splits it — the question a
// metric carrying a reason has to answer.
func collectedByAttr(t *testing.T, reader *sdkmetric.ManualReader, name, attrKey string) map[string]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	out := make(map[string]int64)
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			data, ok := m.Data.(metricdata.Sum[int64])
			if m.Name != name || !ok {
				continue
			}
			for _, dp := range data.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key(attrKey)); ok {
					out[v.Emit()] = dp.Value
				}
			}
		}
	}

	return out
}

// collected runs one manual collection and returns every observed int64
// sum value, keyed by instrument name — enough to assert a metric moved
// without asserting the SDK's full data model.
func collected(t *testing.T, reader *sdkmetric.ManualReader) map[string][]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	out := make(map[string][]int64)
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					out[m.Name] = append(out[m.Name], dp.Value)
				}
			case metricdata.Gauge[int64]:
				for _, dp := range data.DataPoints {
					out[m.Name] = append(out[m.Name], dp.Value)
				}
			}
		}
	}
	return out
}

// collectedFloat mirrors collected for float64 gauges — the staleness gauge
// is the one instrument in this package that isn't an int64 counter.
func collectedFloat(t *testing.T, reader *sdkmetric.ManualReader) map[string][]float64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	out := make(map[string][]float64)
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if data, ok := m.Data.(metricdata.Gauge[float64]); ok {
				for _, dp := range data.DataPoints {
					out[m.Name] = append(out[m.Name], dp.Value)
				}
			}
		}
	}
	return out
}

func meterAndReader() (*sdkmetric.MeterProvider, *sdkmetric.ManualReader) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	return mp, reader
}

// Stub ports the gateway metrics decorators wrap — this package's own copy,
// since internal/metrics/gateway's stubs are unexported to that package.

type staticApps map[string]gateway.App

func (m staticApps) AppForKey(_ context.Context, key string) (gateway.App, bool) {
	app, ok := m[key]
	return app, ok
}

type limiter struct {
	decision gateway.RateDecision
	err      error
}

func (l limiter) Allow(context.Context, string) (gateway.RateDecision, error) {
	return l.decision, l.err
}

type tokenStub struct {
	decision gateway.RateDecision
}

func (t tokenStub) Check(context.Context, string) (gateway.RateDecision, error) {
	return t.decision, nil
}

func (tokenStub) Settle(context.Context, string, int) error { return nil }

type slotStub struct {
	full    bool
	ceiling int
}

func (s slotStub) TryAcquire(string) (release func(), ceiling int, ok bool) {
	if s.full {
		return nil, s.ceiling, false
	}
	return func() {}, 0, true
}

type provider struct {
	completion gateway.Completion
	err        error
}

func (p provider) Complete(context.Context, gateway.ModelProvider, gateway.ChatRequest) (gateway.Completion, error) {
	return p.completion, p.err
}
