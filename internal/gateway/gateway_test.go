package gateway

// Each test is one clause of the MVP Definition of Done: the scenario is the
// opening line; the rule under test is the assertions.

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPrimaryModelProviderServesAndIsDisclosedUnchanged(t *testing.T) {
	gw := newGateway(t)

	res, err := gw.chat("gpt-4.1")

	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "gpt-4.1" || res.Substituted {
		t.Errorf("served = %q (substituted=%v), want the requested gpt-4.1, unsubstituted", res.Model, res.Substituted)
	}
	if !reflect.DeepEqual(gw.provider.calls, []string{"openai"}) {
		t.Errorf("attempts = %v, want exactly one, on the primary", gw.provider.calls)
	}
	if gw.rec.completions != 1 || gw.rec.failedOver {
		t.Errorf("usage recorded = %d completions (failedOver=%v), want 1 without failover", gw.rec.completions, gw.rec.failedOver)
	}
}

func TestUnknownKeyIsRejectedAndNeverForwarded(t *testing.T) {
	gw := newGateway(t)

	_, err := gw.chatAs("no-such-key", "gpt-4.1")

	wantErrorCode(t, err, CodeUnauthorized)
	if len(gw.provider.calls) != 0 {
		t.Errorf("a provider was contacted for an unauthenticated request: %v", gw.provider.calls)
	}
}

func TestMalformedRequestIsRejectedAndNeverForwarded(t *testing.T) {
	gw := newGateway(t)
	req := request("gpt-4.1")
	req.MaxTokens = 0 // required field: it bounds cost per request

	_, err := gw.send("rag-key", req)

	wantErrorCode(t, err, CodeInvalidRequest)
	if len(gw.provider.calls) != 0 {
		t.Errorf("a provider was contacted for a malformed request: %v", gw.provider.calls)
	}
}

func TestOverQuotaIsRejectedWithRetryAfterAndNeverForwarded(t *testing.T) {
	gw := newGateway(t, quotaDenied(2*time.Second, QuotaDetail{Limit: 60, WindowSeconds: 60, Used: 60}))

	_, err := gw.chat("gpt-4.1")

	gwErr := wantErrorCode(t, err, CodeQuotaExceeded)
	if gwErr.RetryAfter != 2*time.Second || gwErr.Quota == nil {
		t.Errorf("rejection = retryAfter %v, quota %+v; want the window that refused, with when to retry", gwErr.RetryAfter, gwErr.Quota)
	}
	if len(gw.provider.calls) != 0 {
		t.Errorf("a provider was contacted for an over-quota request: %v", gw.provider.calls)
	}
}

func TestLimiterOutageFailsOpenAndIsRecorded(t *testing.T) {
	gw := newGateway(t, limiterDown())

	_, err := gw.chat("gpt-4.1")

	if err != nil {
		t.Fatalf("request refused during a limiter outage: %v — availability over enforcement", err)
	}
	if gw.rec.failOpens != 1 {
		t.Errorf("fail-open recorded %d times, want 1 — unmetered must be visible", gw.rec.failOpens)
	}
}

func TestSlotCeilingIsRejectedWithRetryAfterAndNeverForwarded(t *testing.T) {
	gw := newGateway(t, slotsFull(300))

	_, err := gw.chat("gpt-4.1")

	gwErr := wantErrorCode(t, err, CodeConcurrencyCeiling)
	if gwErr.MaxInFlight != 300 || gwErr.RetryAfter <= 0 {
		t.Errorf("rejection = ceiling %d, retryAfter %v; want the ceiling that refused (300) and a positive Retry-After", gwErr.MaxInFlight, gwErr.RetryAfter)
	}
	if len(gw.provider.calls) != 0 {
		t.Errorf("a provider was contacted for an over-ceiling request: %v", gw.provider.calls)
	}
}

func TestSlotIsReleasedExactlyOnce(t *testing.T) {
	t.Run("after success", func(t *testing.T) {
		gw := newGateway(t)
		gw.chat("gpt-4.1")
		if gw.slots.released != 1 {
			t.Errorf("slot released %d times, want 1", gw.slots.released)
		}
	})
	t.Run("after total upstream failure", func(t *testing.T) {
		gw := newGateway(t, failing("openai", FaultServerError), failing("anthropic", FaultServerError))
		gw.chat("gpt-4.1")
		if gw.slots.released != 1 {
			t.Errorf("slot released %d times, want 1", gw.slots.released)
		}
	})
}

func TestFailoverServesTheSubstituteAndDisclosesIt(t *testing.T) {
	gw := newGateway(t, failing("openai", FaultServerError))

	res, err := gw.chat("gpt-4.1") // rag-api: policy any

	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "claude-sonnet-4" || !res.Substituted {
		t.Errorf("served = %q (substituted=%v), want the substitute claude-sonnet-4, disclosed", res.Model, res.Substituted)
	}
	if !reflect.DeepEqual(gw.provider.calls, []string{"openai", "anthropic"}) {
		t.Errorf("attempts = %v, want exactly one on the primary, then the fallback", gw.provider.calls)
	}
	if !gw.rec.failedOver {
		t.Error("failover not recorded")
	}
}

func TestSameModelPolicyRefusesSubstitutes(t *testing.T) {
	gw := newGateway(t, failing("openai", FaultServerError))

	_, err := gw.chatAs("agent-key", "gpt-4.1") // agent-service: same-model, single model provider

	gwErr := wantErrorCode(t, err, CodeModelUnavailable)
	if gwErr.RequestedModel != "gpt-4.1" {
		t.Errorf("requested model = %q, want gpt-4.1 named in the refusal", gwErr.RequestedModel)
	}
	if !reflect.DeepEqual(gw.provider.calls, []string{"openai"}) {
		t.Errorf("attempts = %v, want only the requested model's provider — no substitutes, ever", gw.provider.calls)
	}
}

func TestSameModelPolicyStillGetsProviderFailover(t *testing.T) {
	gw := newGateway(t, modelProviders(gptOpenAI, gptAzure), failing("openai", FaultServerError))

	res, err := gw.chatAs("agent-key", "gpt-4.1") // same model, second host

	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "azure" || res.Model != "gpt-4.1" || res.Substituted {
		t.Errorf("served = %q via %q (substituted=%v), want gpt-4.1 via azure, unsubstituted — provider failover is a transport fact", res.Model, res.Provider, res.Substituted)
	}
}

func TestAllowlistFailsOverToTheAppsNamedSubstitute(t *testing.T) {
	gw := newGateway(t, failing("openai", FaultServerError))

	res, err := gw.chatAs("bot-key", "gpt-4.1") // support-bot allows claude-sonnet-4

	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "claude-sonnet-4" || !res.Substituted {
		t.Errorf("served = %q (substituted=%v), want the app's own named substitute, disclosed", res.Model, res.Substituted)
	}
}

func TestUnknownModelIsUnavailableWithoutAnyAttempt(t *testing.T) {
	gw := newGateway(t)

	_, err := gw.chat("gpt-5") // no model provider serves it — nothing to fail over from

	gwErr := wantErrorCode(t, err, CodeModelUnavailable)
	if gwErr.RequestedModel != "gpt-5" {
		t.Errorf("requested model = %q, want gpt-5 named in the refusal", gwErr.RequestedModel)
	}
	if len(gw.provider.calls) != 0 {
		t.Errorf("a provider was contacted for a model none of them serves: %v", gw.provider.calls)
	}
}

func TestAttemptsAreCappedAtOneFailover(t *testing.T) {
	gw := newGateway(t, failing("openai", FaultServerError), failing("anthropic", FaultUnreachable))

	_, err := gw.chat("gpt-4.1") // policy any: ollama is eligible, but the cap comes first

	wantErrorCode(t, err, CodeUpstreamFailed)
	if !reflect.DeepEqual(gw.provider.calls, []string{"openai", "anthropic"}) {
		t.Errorf("attempts = %v, want the chain cut after one failover — never a cascade eating the caller's budget", gw.provider.calls)
	}
}

func TestProviderThrottlingIsNeverPresentedAsTheCallersQuota(t *testing.T) {
	gw := newGateway(t, failing("openai", FaultThrottled), failing("anthropic", FaultThrottled))

	_, err := gw.chat("gpt-4.1")

	wantErrorCode(t, err, CodeUpstreamFailed) // upstream capacity — not quota_exceeded
}

func TestTimeoutFailoverRecordsDoubleSpendRisk(t *testing.T) {
	gw := newGateway(t, failing("openai", FaultTimeout))

	res, err := gw.chat("gpt-4.1")

	if err != nil || res.Provider != "anthropic" {
		t.Fatalf("served via %q (err=%v), want the fallback", res.Provider, err)
	}
	if !reflect.DeepEqual(gw.rec.doubleSpends, []spend{{"openai", 512}}) {
		t.Errorf("double-spend risk = %v, want the abandoned attempt bounded by max_tokens (512)", gw.rec.doubleSpends)
	}
}

func TestGarbage200TightensTheSpendEstimateToObservedUsage(t *testing.T) {
	gw := newGateway(t, faultedWith("openai", &ProviderFault{Kind: FaultBadResponse, ObservedUsage: &Usage{TotalTokens: 51}}))

	_, err := gw.chat("gpt-4.1")

	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gw.rec.doubleSpends, []spend{{"openai", 51}}) {
		t.Errorf("double-spend risk = %v, want the provider-reported 51 tokens, not max_tokens", gw.rec.doubleSpends)
	}
}

func TestCallerDisconnectAbortsWithoutFailoverAndIsAccounted(t *testing.T) {
	gw := newGateway(t, disconnectingDuring("openai"))

	_, err := gw.chat("gpt-4.1")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the caller's own cancellation", err)
	}
	if !reflect.DeepEqual(gw.provider.calls, []string{"openai"}) {
		t.Errorf("attempts = %v, want no failover — a retry spends tokens nobody receives", gw.provider.calls)
	}
	if !reflect.DeepEqual(gw.rec.disconnects, []spend{{"openai", 512}}) {
		t.Errorf("unobserved spend = %v, want the aborted attempt bounded by max_tokens (512)", gw.rec.disconnects)
	}
}

func TestTotalDeadlineBoundsTheWholeRequest(t *testing.T) {
	gw := newGateway(t, deadlines(80*time.Millisecond, 50*time.Millisecond), hanging("openai"), hanging("anthropic"))

	start := time.Now()
	_, err := gw.chat("gpt-4.1")

	wantErrorCode(t, err, CodeGatewayTimeout)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("request took %v against an 80ms budget — per-try deadlines stacked past it", elapsed)
	}
}

func TestNewRefusesDeadlinesThatCouldStack(t *testing.T) {
	gw := newGateway(t) // donor of valid deps

	_, err := New(Config{
		ModelProviders: gw.cfg.ModelProviders,
		FailoverOrder:  gw.cfg.FailoverOrder,
		TotalDeadline:  time.Second,
		PerTryDeadline: time.Second, // == total: one slow try eats the whole budget
	}, Deps{Apps: gw.apps, RateLimiter: gw.limiter, Slots: gw.slots, Provider: gw.provider})

	if err == nil {
		t.Error("New accepted PerTryDeadline >= TotalDeadline")
	}
}
