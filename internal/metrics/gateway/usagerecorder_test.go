package metrics

import (
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestCompletionCountsTokensAndFailoverAndForwardsToNext(t *testing.T) {
	next := &recordedUsage{}
	r := NewUsageRecorder(next)
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
	r := NewUsageRecorder(&recordedUsage{})

	r.RecordCompletion("rag-api", gptOpenAI, gateway.Usage{}, time.Second, false)

	if r.FailedOvers() != 0 {
		t.Errorf("FailedOvers() = %d, want 0", r.FailedOvers())
	}
}

func TestRejectionCountsByCodeAndForwardsToNext(t *testing.T) {
	next := &recordedUsage{}
	r := NewUsageRecorder(next)

	r.RecordRejection("rag-api", gateway.CodeQuotaExceeded)

	if r.Rejections() != 1 {
		t.Errorf("Rejections() = %d, want 1", r.Rejections())
	}
	if got := r.RejectionsByCode(gateway.CodeQuotaExceeded); got != 1 {
		t.Errorf("RejectionsByCode(quota_exceeded) = %d, want 1", got)
	}
	if got := r.RejectionsByCode(gateway.CodeUnauthorized); got != 0 {
		t.Errorf("RejectionsByCode(unauthorized) = %d, want 0 — the wrong code must not bleed into another's count", got)
	}
	if next.rejections != 1 {
		t.Error("the wrapped recorder never saw the rejection")
	}
}

func TestFailOpenDoubleSpendAndDisconnectEachCountIndependently(t *testing.T) {
	r := NewUsageRecorder(&recordedUsage{})

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
	r := NewUsageRecorder(&recordedUsage{})

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
	r := NewUsageRecorder(&recordedUsage{})

	r.RecordDoubleSpendRisk("rag-api", gptOpenAI, 512)

	if r.ClientDisconnectTokens() != 0 {
		t.Errorf("ClientDisconnectTokens() = %d, want 0 — a double-spend estimate bled into the disconnect sum", r.ClientDisconnectTokens())
	}
}

func TestANonPositiveEstimateNeverRunsTheCounterBackwards(t *testing.T) {
	// max_tokens is validated at the edge, so this should be unreachable —
	// but these feed monotonic counters, where a single negative would
	// corrupt every rate computed over the series and leave no trace of why.
	r := NewUsageRecorder(&recordedUsage{})

	r.RecordDoubleSpendRisk("rag-api", gptOpenAI, 0)
	r.RecordDoubleSpendRisk("rag-api", gptOpenAI, -5)

	if r.DoubleSpendTokens() != 0 {
		t.Errorf("DoubleSpendTokens() = %d, want 0", r.DoubleSpendTokens())
	}
	if r.DoubleSpendRisks() != 2 {
		t.Errorf("DoubleSpendRisks() = %d, want 2 — the event still happened even when its estimate is unusable", r.DoubleSpendRisks())
	}
}
