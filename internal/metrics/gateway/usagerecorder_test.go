package metrics

import (
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// configuredApps is the closed set these tests build against — the same
// shape config supplies at boot.
var configuredApps = []string{"rag-api", "agent-service"}

func TestCompletionCountsTokensAndFailoverAndForwardsToNext(t *testing.T) {
	next := &recordedUsage{}
	r := NewUsageRecorder(next, configuredApps)
	usage := gateway.Usage{PromptTokens: 21, CompletionTokens: 30, TotalTokens: 51}

	r.RecordCompletion("rag-api", gptOpenAI, usage, 250*time.Millisecond, true)

	if r.Completions() != 1 || r.FailedOvers() != 1 {
		t.Errorf("completions=%d failedOvers=%d, want both counted", r.Completions(), r.FailedOvers())
	}
	if r.PromptTokens() != 21 || r.CompletionTokens() != 30 {
		t.Errorf("promptTokens=%d completionTokens=%d, want the usage's own totals", r.PromptTokens(), r.CompletionTokens())
	}
	if next.completions != 1 {
		t.Error("the wrapped recorder never saw the completion — forwarding must be unconditional")
	}
}

func TestNonFailoverCompletionDoesNotCountAsAFailover(t *testing.T) {
	r := NewUsageRecorder(&recordedUsage{}, configuredApps)

	r.RecordCompletion("rag-api", gptOpenAI, gateway.Usage{}, time.Second, false)

	if r.FailedOvers() != 0 {
		t.Errorf("FailedOvers() = %d, want 0", r.FailedOvers())
	}
}

func TestRejectionCountsByCodeAndForwardsToNext(t *testing.T) {
	next := &recordedUsage{}
	r := NewUsageRecorder(next, configuredApps)

	r.RecordRejection("rag-api", gateway.CodeQuotaExceeded)

	if r.Rejections() != 1 {
		t.Errorf("Rejections() = %d, want 1", r.Rejections())
	}
	if got := r.RejectionsBy("rag-api", gateway.CodeQuotaExceeded); got != 1 {
		t.Errorf("RejectionsBy(rag-api, quota_exceeded) = %d, want 1", got)
	}
	if got := r.RejectionsBy("rag-api", gateway.CodeUnauthorized); got != 0 {
		t.Errorf("RejectionsBy(rag-api, unauthorized) = %d, want 0 — the wrong code must not bleed into another's count", got)
	}
	if got := r.RejectionsBy("agent-service", gateway.CodeQuotaExceeded); got != 0 {
		t.Errorf("RejectionsBy(agent-service, quota_exceeded) = %d, want 0 — one app's refusal must not land on another", got)
	}
	if next.rejections != 1 {
		t.Error("the wrapped recorder never saw the rejection")
	}
}

func TestFailOpenDoubleSpendAndDisconnectEachCountIndependently(t *testing.T) {
	r := NewUsageRecorder(&recordedUsage{}, configuredApps)

	r.RecordRateLimiterFailOpen("rag-api")
	r.RecordDoubleSpendRisk("rag-api", gptOpenAI, 512)
	r.RecordClientDisconnect("rag-api", gptOpenAI, 512)

	if r.RateLimiterFailOpens() != 1 {
		t.Errorf("RateLimiterFailOpens() = %d, want 1", r.RateLimiterFailOpens())
	}
	if r.DoubleSpendRisks() != 1 {
		t.Errorf("DoubleSpendRisks() = %d, want 1", r.DoubleSpendRisks())
	}
	if r.ClientDisconnects() != 1 {
		t.Errorf("ClientDisconnects() = %d, want 1", r.ClientDisconnects())
	}
}

func TestUnobservedSpendIsSummedInTokensNotOnlyCountedInEvents(t *testing.T) {
	// The event counts say an incident happened; only the token sums say
	// whether it was material against a real bill. Reconciliation subtracts
	// the tokens, so a recorder that keeps just the counts leaves that
	// criterion with nothing to subtract.
	r := NewUsageRecorder(&recordedUsage{}, configuredApps)

	r.RecordDoubleSpendRisk("rag-api", gptOpenAI, 512)
	r.RecordDoubleSpendRisk("rag-api", gptOpenAI, 128)
	r.RecordClientDisconnect("rag-api", gptOpenAI, 256)

	if r.DoubleSpendTokens() != 640 {
		t.Errorf("DoubleSpendTokens() = %d, want 640 — every event's estimate accumulates", r.DoubleSpendTokens())
	}
	if r.ClientDisconnectTokens() != 256 {
		t.Errorf("ClientDisconnectTokens() = %d, want 256", r.ClientDisconnectTokens())
	}
	if r.DoubleSpendRisks() != 2 || r.ClientDisconnects() != 1 {
		t.Errorf("risks=%d disconnects=%d, want 2 and 1 — the counts stay independent of the sums",
			r.DoubleSpendRisks(), r.ClientDisconnects())
	}
}

func TestTheTwoUnobservedSpendReasonsNeverPoolTheirTokens(t *testing.T) {
	// Decision #3 and decision #6 are different trades: one is spend the
	// caller abandoned, the other spend this gateway chose to risk. An
	// operator reading an incident has to be able to tell them apart.
	r := NewUsageRecorder(&recordedUsage{}, configuredApps)

	r.RecordDoubleSpendRisk("rag-api", gptOpenAI, 512)

	if r.ClientDisconnectTokens() != 0 {
		t.Errorf("ClientDisconnectTokens() = %d, want 0 — a double-spend estimate bled into the disconnect sum", r.ClientDisconnectTokens())
	}
}

func TestANonPositiveEstimateNeverRunsTheCounterBackwards(t *testing.T) {
	// max_tokens is validated at the edge, so this should be unreachable —
	// but these feed monotonic counters, where a single negative would
	// corrupt every rate computed over the series and leave no trace of why.
	r := NewUsageRecorder(&recordedUsage{}, configuredApps)

	r.RecordDoubleSpendRisk("rag-api", gptOpenAI, 0)
	r.RecordDoubleSpendRisk("rag-api", gptOpenAI, -5)

	if r.DoubleSpendTokens() != 0 {
		t.Errorf("DoubleSpendTokens() = %d, want 0", r.DoubleSpendTokens())
	}
	if r.DoubleSpendRisks() != 2 {
		t.Errorf("DoubleSpendRisks() = %d, want 2 — the event still happened even when its estimate is unusable", r.DoubleSpendRisks())
	}
}

func TestSpendIsAttributedToTheAppThatCausedIt(t *testing.T) {
	// The design's own promise — "spend is traceable per app, and it
	// reconciles with the provider bills" — is unmeasurable from a
	// process-wide total, because a bill is per account and a budget is per
	// app. This is the series that makes that criterion runnable.
	r := NewUsageRecorder(&recordedUsage{}, configuredApps)

	r.RecordCompletion("rag-api", gptOpenAI, gateway.Usage{PromptTokens: 21, CompletionTokens: 30}, 0, false)
	r.RecordCompletion("agent-service", gptOpenAI, gateway.Usage{PromptTokens: 100, CompletionTokens: 200}, 0, true)

	if got := r.PromptTokensByApp("rag-api"); got != 21 {
		t.Errorf("PromptTokensByApp(rag-api) = %d, want 21", got)
	}
	if got := r.CompletionTokensByApp("agent-service"); got != 200 {
		t.Errorf("CompletionTokensByApp(agent-service) = %d, want 200", got)
	}
	if got := r.FailedOversByApp("rag-api"); got != 0 {
		t.Errorf("FailedOversByApp(rag-api) = %d, want 0 — agent-service's failover was attributed to rag-api", got)
	}
	if r.PromptTokens() != 121 {
		t.Errorf("PromptTokens() = %d, want 121 — the total is the sum of the parts, never a separate counter", r.PromptTokens())
	}
}

func TestAnUnconfiguredAppIsDroppedRatherThanCreatingASeries(t *testing.T) {
	// The cardinality bound is structural, not a convention callers keep: an
	// app outside the configured set cannot mint a counter. It cannot happen
	// in practice either — an unresolved key never becomes an app — but the
	// type is what guarantees a deployment's series count is readable off its
	// config file.
	r := NewUsageRecorder(&recordedUsage{}, configuredApps)

	r.RecordCompletion("never-configured", gptOpenAI, gateway.Usage{PromptTokens: 999}, 0, false)

	if got := r.PromptTokensByApp("never-configured"); got != 0 {
		t.Errorf("PromptTokensByApp(never-configured) = %d, want 0", got)
	}
	if r.PromptTokens() != 0 {
		t.Errorf("PromptTokens() = %d, want 0 — an unattributable record must not reach any series", r.PromptTokens())
	}
	if len(r.Apps()) != len(configuredApps) {
		t.Errorf("Apps() = %v, want only the configured set", r.Apps())
	}
}

func TestFailOpenNamesTheAppWhoseLimitsStoppedApplying(t *testing.T) {
	// Fail-open produces no error and no latency, so this counter is one of
	// the few signals that a limiter is down — and which app is running
	// unmetered is the actionable half.
	r := NewUsageRecorder(&recordedUsage{}, configuredApps)

	r.RecordRateLimiterFailOpen("agent-service")

	if got := r.RateLimiterFailOpensByApp("agent-service"); got != 1 {
		t.Errorf("RateLimiterFailOpensByApp(agent-service) = %d, want 1", got)
	}
	if got := r.RateLimiterFailOpensByApp("rag-api"); got != 0 {
		t.Errorf("RateLimiterFailOpensByApp(rag-api) = %d, want 0", got)
	}
}
