package otlp

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	gwmetrics "github.com/FernasFragas/LLMGateway-Go/internal/metrics/gateway"
)

// gatewayFaultKinds and gatewayErrorCodes are the fixed label sets the
// callbacks below enumerate — both are closed sets declared in
// internal/gateway, not open-ended strings, so cardinality stays bounded
// regardless of traffic.
var gatewayFaultKinds = []gateway.FaultKind{
	gateway.FaultUnreachable,
	gateway.FaultTimeout,
	gateway.FaultServerError,
	gateway.FaultThrottled,
	gateway.FaultBadResponse,
	gateway.FaultRejected,
}

var gatewayErrorCodes = []gateway.ErrorCode{
	gateway.CodeInvalidRequest,
	gateway.CodeUnauthorized,
	gateway.CodeQuotaExceeded,
	gateway.CodeConcurrencyCeiling,
	gateway.CodeUpstreamFailed,
	gateway.CodeModelUnavailable,
	gateway.CodeGatewayTimeout,
}

// RegisterGateway attaches observable instruments over the gateway core's
// metrics decorators. Every callback only reads accessor methods the
// counters already exposed for a scrape — nothing here changes what they
// track. Per-app and per-provider breakdowns are not available: the
// counters as built track only fixed-cardinality dimensions (fault kind,
// error code); a labeled llm_tokens_total{app,provider} would need the
// counters themselves to grow an app/provider-keyed map, which is a
// separate decision from wiring the exporter this ADR describes.
func RegisterGateway(meter metric.Meter, apps *gwmetrics.AppDirectory, rl *gwmetrics.RateLimiter, tl *gwmetrics.TokenLimiter, sl *gwmetrics.SlotLimiter, pc *gwmetrics.ProviderClient, usage *gwmetrics.UsageRecorder) error {
	if _, err := meter.Int64ObservableCounter("gateway_key_resolved_total",
		metric.WithDescription("API keys resolved to a known app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(apps.Resolved())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_key_refused_total",
		metric.WithDescription("API keys that matched no configured app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(apps.Refused())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_ratelimit_allowed_total",
		metric.WithDescription("requests the rate limiter admitted"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(rl.Allowed())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_ratelimit_denied_total",
		metric.WithDescription("requests a quota refused"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(rl.Denied())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_ratelimit_fail_open_total",
		metric.WithDescription("requests admitted unmetered because the rate limiter itself failed"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(rl.FailOpen())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_token_budget_allowed_total",
		metric.WithDescription("requests admitted with token budget left"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(tl.Allowed())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_token_budget_denied_total",
		metric.WithDescription("requests a spent token budget refused"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(tl.Denied())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_token_budget_fail_open_total",
		metric.WithDescription("requests admitted unmetered because the token limiter itself failed"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(tl.FailOpen())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_tokens_debited_total",
		metric.WithDescription("tokens successfully charged against per-app budgets"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(tl.Debited())
			return nil
		}),
	); err != nil {
		return err
	}

	// Distinct from fail-open on purpose (ADR-003): the request succeeded and
	// only the accounting was lost, so a window under-charges until it rolls.
	// Sharing the fail-open series would read as an outage that never happened.
	if _, err := meter.Int64ObservableCounter("gateway_token_debit_failed_total",
		metric.WithDescription("served completions whose cost never reached the budget"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(tl.DebitsFailed())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_slots_acquired_total",
		metric.WithDescription("in-flight slots granted"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(sl.Acquired())
			return nil
		}),
	); err != nil {
		return err
	}

	// concurrency_limit_rejections_total is the exact name the architecture
	// brief's slot-ceiling acceptance criterion names.
	if _, err := meter.Int64ObservableCounter("concurrency_limit_rejections_total",
		metric.WithDescription("requests refused because an app's slot ceiling was full"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(sl.Refused())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("provider_attempts_total",
		metric.WithDescription("provider call attempts, served or failed"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(pc.Attempts())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("provider_failures_total",
		metric.WithDescription("provider call attempts that failed, by fault kind"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, kind := range gatewayFaultKinds {
				if n := pc.FailuresByKind(kind); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("kind", string(kind))))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("llm_completions_total",
		metric.WithDescription("chat completions served"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(usage.Completions())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("llm_failed_overs_total",
		metric.WithDescription("served completions that required a failover"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(usage.FailedOvers())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("llm_prompt_tokens_total",
		metric.WithDescription("prompt tokens across every served completion"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(usage.PromptTokens())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("llm_completion_tokens_total",
		metric.WithDescription("completion tokens across every served completion"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(usage.CompletionTokens())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_rejections_total",
		metric.WithDescription("requests refused before reaching a provider, by error code"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, code := range gatewayErrorCodes {
				if n := usage.RejectionsByCode(code); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("code", string(code))))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_ratelimit_fail_open_admitted_total",
		metric.WithDescription("completions recorded while the rate limiter was failing open"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(usage.RateLimiterFailOpens())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("double_spend_risk_total",
		metric.WithDescription("failovers that risked billing an abandoned attempt twice"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(usage.DoubleSpendRisks())
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("client_disconnects_total",
		metric.WithDescription("requests a caller abandoned mid-flight"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(usage.ClientDisconnects())
			return nil
		}),
	); err != nil {
		return err
	}

	return nil
}
