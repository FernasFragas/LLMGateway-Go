package otlp

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	gwmetrics "github.com/FernasFragas/LLMGateway-Go/internal/metrics/gateway"
)

// RegisterGateway attaches observable instruments over the gateway core's
// metrics decorators. Every callback only reads accessor methods the
// counters already exposed — nothing here changes what they track.
//
// Every dimension is enumerated from the counters' own key sets (Apps,
// Providers, gwmetrics.ErrorCodes, gwmetrics.FaultKinds) rather than from a
// list kept here, so the exporter cannot observe a series the storage does
// not have or miss one it does. A key with no activity is skipped: an
// untouched label reads as absent rather than as a zero, so a dashboard
// shows the apps and providers that actually did something.
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
		metric.WithDescription("in-flight slots granted, by app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range sl.Apps() {
				if n := sl.AcquiredByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("app", app)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	// concurrency_limit_rejections_total is the exact name the architecture
	// brief's slot-ceiling acceptance criterion names, and it carries the app
	// for the same reason the criterion does: the ceiling is per app, and
	// "somebody is saturated" is not something an operator can act on.
	if _, err := meter.Int64ObservableCounter("concurrency_limit_rejections_total",
		metric.WithDescription("requests refused because an app's slot ceiling was full, by app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range sl.Apps() {
				if n := sl.RefusedByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("app", app)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("provider_attempts_total",
		metric.WithDescription("provider call attempts, served or failed, by provider"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, provider := range pc.Providers() {
				if n := pc.AttemptsByProvider(provider); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("provider", provider)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("provider_failures_total",
		metric.WithDescription("provider call attempts that failed, by provider and fault kind"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, provider := range pc.Providers() {
				for _, kind := range gwmetrics.FaultKinds {
					if n := pc.FailuresBy(provider, kind); n > 0 {
						o.Observe(n, metric.WithAttributes(
							attribute.String("provider", provider),
							attribute.String("kind", string(kind)),
						))
					}
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("llm_completions_total",
		metric.WithDescription("chat completions served, by app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range usage.Apps() {
				if n := usage.CompletionsByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("app", app)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("llm_failed_overs_total",
		metric.WithDescription("served completions that required a failover, by app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range usage.Apps() {
				if n := usage.FailedOversByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("app", app)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("llm_prompt_tokens_total",
		metric.WithDescription("prompt tokens across every served completion, by app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range usage.Apps() {
				if n := usage.PromptTokensByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("app", app)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("llm_completion_tokens_total",
		metric.WithDescription("completion tokens across every served completion, by app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range usage.Apps() {
				if n := usage.CompletionTokensByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("app", app)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_rejections_total",
		metric.WithDescription("requests refused before reaching a provider, by app and error code"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range usage.Apps() {
				for _, code := range gwmetrics.ErrorCodes {
					if n := usage.RejectionsBy(app, code); n > 0 {
						o.Observe(n, metric.WithAttributes(
							attribute.String("app", app),
							attribute.String("code", string(code)),
						))
					}
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("gateway_ratelimit_fail_open_admitted_total",
		metric.WithDescription("requests admitted unmetered while a limiter was failing open, by app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range usage.Apps() {
				if n := usage.RateLimiterFailOpensByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("app", app)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("double_spend_risk_total",
		metric.WithDescription("failovers that risked billing an abandoned attempt twice, by app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range usage.Apps() {
				if n := usage.DoubleSpendRisksByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("app", app)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	if _, err := meter.Int64ObservableCounter("client_disconnects_total",
		metric.WithDescription("requests a caller abandoned mid-flight, by app"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range usage.Apps() {
				if n := usage.ClientDisconnectsByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(attribute.String("app", app)))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	// The token companion to the two counters above. They report that spend
	// went unobserved; only this reports how much, which is the term the
	// brief's billing-reconciliation criterion subtracts — without it that
	// criterion has no denominator and cannot be evaluated at all.
	//
	// One series with a bounded reason rather than two metrics: the
	// reconciliation query wants the total and should not have to remember
	// both names, while an operator reading an incident wants them apart,
	// because they are different decisions — a disconnect is spend the caller
	// abandoned (decision #3), a double-spend risk is spend this gateway
	// chose to risk to buy availability (decision #6).
	if _, err := meter.Int64ObservableCounter("unobserved_spend_tokens_estimate",
		metric.WithDescription("upper-bound tokens a provider may have billed that the gateway never received, by app and reason"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for _, app := range usage.Apps() {
				if n := usage.DoubleSpendTokensByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(
						attribute.String("app", app),
						attribute.String("reason", "double_spend"),
					))
				}
				if n := usage.ClientDisconnectTokensByApp(app); n > 0 {
					o.Observe(n, metric.WithAttributes(
						attribute.String("app", app),
						attribute.String("reason", "client_disconnect"),
					))
				}
			}
			return nil
		}),
	); err != nil {
		return err
	}

	return nil
}
