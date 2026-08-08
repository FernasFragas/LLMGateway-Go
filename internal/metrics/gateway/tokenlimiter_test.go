package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestBudgetLeftCountsAsAllowed(t *testing.T) {
	lim := NewTokenLimiter(&tokenLimiter{decision: gateway.RateDecision{Allowed: true}})

	if _, err := lim.Check(context.Background(), "rag-api"); err != nil {
		t.Fatal(err)
	}

	if lim.Allowed() != 1 || lim.Denied() != 0 || lim.FailOpen() != 0 {
		t.Errorf("allowed=%d denied=%d failOpen=%d, want only allowed counted",
			lim.Allowed(), lim.Denied(), lim.FailOpen())
	}
}

func TestASpentBudgetCountsAsDenied(t *testing.T) {
	lim := NewTokenLimiter(&tokenLimiter{decision: gateway.RateDecision{Allowed: false}})

	_, _ = lim.Check(context.Background(), "rag-api")

	if lim.Denied() != 1 || lim.Allowed() != 0 {
		t.Errorf("allowed=%d denied=%d, want only denied counted", lim.Allowed(), lim.Denied())
	}
}

func TestACheckOutageCountsAsFailOpenNotADecision(t *testing.T) {
	outage := errors.New("redis: connection refused")
	lim := NewTokenLimiter(&tokenLimiter{checkErr: outage})

	_, err := lim.Check(context.Background(), "rag-api")

	if !errors.Is(err, outage) {
		t.Errorf("error = %v, want the limiter's own, unchanged", err)
	}
	if lim.FailOpen() != 1 {
		t.Errorf("FailOpen() = %d, want 1", lim.FailOpen())
	}
	if lim.Allowed() != 0 || lim.Denied() != 0 {
		t.Errorf("allowed=%d denied=%d, want an outage counted as neither", lim.Allowed(), lim.Denied())
	}
}

func TestDebitedTokensAccumulateAndReachTheLimiter(t *testing.T) {
	inner := &tokenLimiter{}
	lim := NewTokenLimiter(inner)

	_ = lim.Settle(context.Background(), "rag-api", 51)
	_ = lim.Settle(context.Background(), "rag-api", 30)

	if lim.Debited() != 81 {
		t.Errorf("Debited() = %d, want the 81 tokens both completions cost", lim.Debited())
	}
	if len(inner.settled) != 2 || inner.settled[0] != 51 {
		t.Errorf("forwarded %v, want each cost passed through unchanged", inner.settled)
	}
}

func TestALostDebitIsCountedSeparatelyFromAFailOpen(t *testing.T) {
	// A failed debit is not an outage of enforcement: the request succeeded
	// and only the accounting was lost. Sharing FailOpen's counter would read
	// as a limiter outage that never happened.
	outage := errors.New("redis: connection refused")
	lim := NewTokenLimiter(&tokenLimiter{settleErr: outage})

	err := lim.Settle(context.Background(), "rag-api", 51)

	if !errors.Is(err, outage) {
		t.Errorf("error = %v, want the limiter's own, unchanged", err)
	}
	if lim.DebitsFailed() != 1 {
		t.Errorf("DebitsFailed() = %d, want 1", lim.DebitsFailed())
	}
	if lim.FailOpen() != 0 {
		t.Errorf("FailOpen() = %d, want a lost debit counted as its own kind of failure", lim.FailOpen())
	}
	if lim.Debited() != 0 {
		t.Errorf("Debited() = %d, want tokens that never reached the budget left uncounted", lim.Debited())
	}
}
