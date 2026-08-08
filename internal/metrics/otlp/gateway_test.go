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

	sl := gwmetrics.NewSlotLimiter(slotStub{full: true, ceiling: 300})
	_, _, _ = sl.TryAcquire("rag-api")

	pc := gwmetrics.NewProviderClient(provider{err: &gateway.ProviderFault{Kind: gateway.FaultTimeout}})
	_, _ = pc.Complete(context.Background(), gateway.ModelProvider{}, gateway.ChatRequest{})

	usage := gwmetrics.NewUsageRecorder(gateway.NopUsageRecorder{})
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
		t.Errorf("provider_failures_total = %v, want exactly one data point valued 1 (the timeout kind)", kindValues)
	}
}
