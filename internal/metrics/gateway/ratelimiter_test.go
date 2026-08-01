package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestAllowedDecisionCountsAsAllowed(t *testing.T) {
	lim := NewRateLimiter(limiter{decision: gateway.RateDecision{Allowed: true}})

	_, err := lim.Allow(context.Background(), "rag-api")

	if err != nil {
		t.Fatal(err)
	}
	if lim.Allowed() != 1 || lim.Denied() != 0 || lim.FailOpen() != 0 {
		t.Errorf("allowed=%d denied=%d failOpen=%d, want only allowed counted", lim.Allowed(), lim.Denied(), lim.FailOpen())
	}
}

func TestDeniedDecisionCountsAsDenied(t *testing.T) {
	lim := NewRateLimiter(limiter{decision: gateway.RateDecision{Allowed: false}})

	_, _ = lim.Allow(context.Background(), "rag-api")

	if lim.Denied() != 1 || lim.Allowed() != 0 {
		t.Errorf("allowed=%d denied=%d, want only denied counted", lim.Allowed(), lim.Denied())
	}
}

func TestLimiterOutageCountsAsFailOpenNotADecisionAndReturnsTheError(t *testing.T) {
	outage := errors.New("redis: connection refused")
	lim := NewRateLimiter(limiter{err: outage})

	_, err := lim.Allow(context.Background(), "rag-api")

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
