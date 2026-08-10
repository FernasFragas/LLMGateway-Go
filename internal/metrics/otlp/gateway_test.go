package otlp

import (
	"context"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	gwmetrics "github.com/FernasFragas/LLMGateway-Go/internal/metrics/gateway"
)

func TestRegisterGatewayReportsEveryCounterAtItsCurrentValue(t *testing.T) {
	mp, reader := meterAndReader()

	apps := gwmetrics.NewAppDirectory(staticApps{"sk-live": {Name: "rag-api"}})
	_, _ = apps.AppForKey(context.Background(), "sk-live")
	_, _ = apps.AppForKey(context.Background(), "sk-unknown")

	rl := gwmetrics.NewRateLimiter(limiter{decision: gateway.RateDecision{Allowed: true}})
	_, _ = rl.Allow(context.Background(), "rag-api")

	tl := gwmetrics.NewTokenLimiter(tokenStub{decision: gateway.RateDecision{Allowed: true}})
	_, _ = tl.Check(context.Background(), "rag-api")
	_ = tl.Settle(context.Background(), "rag-api", 15)

	sl := gwmetrics.NewSlotLimiter(slotStub{full: true, ceiling: 300}, testApps)
	_, _, _ = sl.TryAcquire("rag-api")

	pc := gwmetrics.NewProviderClient(provider{err: &gateway.ProviderFault{Kind: gateway.FaultTimeout}}, testProviders)
	_, _ = pc.Complete(context.Background(), gateway.ModelProvider{Provider: "openai"}, gateway.ChatRequest{})

	usage := gwmetrics.NewUsageRecorder(gateway.NopUsageRecorder{}, testApps)
	usage.RecordCompletion("rag-api", gateway.ModelProvider{}, gateway.Usage{PromptTokens: 10, CompletionTokens: 5}, 0, false)

	if err := RegisterGateway(mp.Meter("test"), apps, rl, tl, sl, pc, usage); err != nil {
		t.Fatalf("RegisterGateway: %v", err)
	}

	got := collected(t, reader)
	cases := map[string]int64{
		"gateway_key_resolved_total":         1,
		"gateway_key_refused_total":          1,
		"gateway_ratelimit_allowed_total":    1,
		"gateway_token_budget_allowed_total": 1,
		"gateway_tokens_debited_total":       15,
		"concurrency_limit_rejections_total": 1,
		"provider_attempts_total":            1,
		"llm_prompt_tokens_total":            10,
		"llm_completion_tokens_total":        5,
	}
	for name, want := range cases {
		values, ok := got[name]
		if !ok || len(values) == 0 || values[0] != want {
			t.Errorf("%s = %v, want [%d]", name, values, want)
		}
	}

	kindValues, ok := got["provider_failures_total"]
	if !ok || len(kindValues) != 1 || kindValues[0] != 1 {
		t.Errorf("provider_failures_total = %v, want exactly one data point valued 1 (openai's timeout)", kindValues)
	}
}

func TestTheUnobservedSpendEstimateIsReportedByReason(t *testing.T) {
	// One series, split by a bounded reason: reconciliation sums it without
	// having to remember two metric names, and an operator can still see
	// which trade produced the tokens.
	mp, reader := meterAndReader()

	usage := gwmetrics.NewUsageRecorder(gateway.NopUsageRecorder{}, testApps)
	usage.RecordDoubleSpendRisk("rag-api", gateway.ModelProvider{}, 512)
	usage.RecordClientDisconnect("rag-api", gateway.ModelProvider{}, 128)

	registerUsageOnly(t, mp.Meter("test"), usage)

	byReason := collectedByAttr(t, reader, "unobserved_spend_tokens_estimate", "reason")
	if byReason["double_spend"] != 512 {
		t.Errorf("reason=double_spend = %d, want 512", byReason["double_spend"])
	}
	if byReason["client_disconnect"] != 128 {
		t.Errorf("reason=client_disconnect = %d, want 128", byReason["client_disconnect"])
	}
}

func TestAReasonWithNoSpendIsNotReportedAtAll(t *testing.T) {
	// Same rule the rejection and fault-kind callbacks follow: an untouched
	// label is absent rather than a zero, so a dashboard shows the reasons
	// that actually happened.
	mp, reader := meterAndReader()

	usage := gwmetrics.NewUsageRecorder(gateway.NopUsageRecorder{}, testApps)
	usage.RecordDoubleSpendRisk("rag-api", gateway.ModelProvider{}, 512)

	registerUsageOnly(t, mp.Meter("test"), usage)

	byReason := collectedByAttr(t, reader, "unobserved_spend_tokens_estimate", "reason")
	if _, present := byReason["client_disconnect"]; present {
		t.Errorf("reason=client_disconnect reported %d with no disconnect recorded", byReason["client_disconnect"])
	}
}

func TestTokenSpendIsExportedPerApp(t *testing.T) {
	// The series the brief's reconciliation criterion is written against:
	// sum(llm_prompt_tokens_total{app="rag-api"}). Without the label the
	// criterion has no query, whatever the totals say.
	mp, reader := meterAndReader()

	usage := gwmetrics.NewUsageRecorder(gateway.NopUsageRecorder{}, testApps)
	usage.RecordCompletion("rag-api", gateway.ModelProvider{Provider: "openai"},
		gateway.Usage{PromptTokens: 21, CompletionTokens: 30}, 0, false)
	usage.RecordCompletion("agent-service", gateway.ModelProvider{Provider: "openai"},
		gateway.Usage{PromptTokens: 100, CompletionTokens: 200}, 0, false)

	registerUsageOnly(t, mp.Meter("test"), usage)

	prompt := collectedByAttr(t, reader, "llm_prompt_tokens_total", "app")
	if prompt["rag-api"] != 21 || prompt["agent-service"] != 100 {
		t.Errorf("llm_prompt_tokens_total by app = %v, want rag-api 21 and agent-service 100", prompt)
	}

	completion := collectedByAttr(t, reader, "llm_completion_tokens_total", "app")
	if completion["rag-api"] != 30 || completion["agent-service"] != 200 {
		t.Errorf("llm_completion_tokens_total by app = %v, want rag-api 30 and agent-service 200", completion)
	}
}

func TestAnAppWithNoActivityProducesNoSeries(t *testing.T) {
	// A configured app that did nothing is absent rather than zero, the same
	// convention every other breakdown here follows — a dashboard shows the
	// apps that actually ran.
	mp, reader := meterAndReader()

	usage := gwmetrics.NewUsageRecorder(gateway.NopUsageRecorder{}, testApps)
	usage.RecordCompletion("rag-api", gateway.ModelProvider{Provider: "openai"},
		gateway.Usage{PromptTokens: 21}, 0, false)

	registerUsageOnly(t, mp.Meter("test"), usage)

	prompt := collectedByAttr(t, reader, "llm_prompt_tokens_total", "app")
	if _, present := prompt["agent-service"]; present {
		t.Errorf("agent-service reported %d prompt tokens having served nothing", prompt["agent-service"])
	}
}

func TestUpstreamFailuresAreExportedPerProvider(t *testing.T) {
	// "Upstream failures are up" is not actionable; "anthropic's timeouts are
	// up" is. Both labels have to survive to the wire for that to be true.
	mp, reader := meterAndReader()

	pc := gwmetrics.NewProviderClient(provider{err: &gateway.ProviderFault{Kind: gateway.FaultTimeout}}, testProviders)
	_, _ = pc.Complete(context.Background(), gateway.ModelProvider{Provider: "anthropic"}, gateway.ChatRequest{})

	err := RegisterGateway(mp.Meter("test"),
		gwmetrics.NewAppDirectory(staticApps{}),
		gwmetrics.NewRateLimiter(limiter{}),
		gwmetrics.NewTokenLimiter(tokenStub{}),
		gwmetrics.NewSlotLimiter(slotStub{}, testApps),
		pc,
		gwmetrics.NewUsageRecorder(gateway.NopUsageRecorder{}, testApps),
	)
	if err != nil {
		t.Fatalf("RegisterGateway: %v", err)
	}

	byProvider := collectedByAttr(t, reader, "provider_failures_total", "provider")
	if byProvider["anthropic"] != 1 {
		t.Errorf("provider_failures_total by provider = %v, want anthropic 1", byProvider)
	}
	if _, present := byProvider["openai"]; present {
		t.Error("openai reported a failure it never had — the provider label is not separating the series")
	}

	byKind := collectedByAttr(t, reader, "provider_failures_total", "kind")
	if byKind["timeout"] != 1 {
		t.Errorf("provider_failures_total by kind = %v, want timeout 1 — both labels must survive together", byKind)
	}
}
