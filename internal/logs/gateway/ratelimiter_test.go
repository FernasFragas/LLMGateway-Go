package logs

import (
	"context"
	"errors"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestLimiterOutageCauseIsLoggedAndTheErrorStillReturned(t *testing.T) {
	log, out := captured(t)
	outage := errors.New("redis: connection refused")
	lim := NewRateLimiter(limiter{err: outage}, log)

	_, err := lim.Allow(context.Background(), "rag-api")

	if !errors.Is(err, outage) {
		t.Errorf("error = %v, want the limiter's own, unchanged — the decorator observes, never handles", err)
	}
	wantLogged(t, out, "rag-api", "redis: connection refused")
}

func TestAllowedDecisionPassesThroughSilently(t *testing.T) {
	log, out := captured(t)
	lim := NewRateLimiter(limiter{decision: gateway.RateDecision{Allowed: true}}, log)

	decision, err := lim.Allow(context.Background(), "rag-api")

	if err != nil || !decision.Allowed {
		t.Fatalf("decision = %+v (err=%v), want allowed passed through unchanged", decision, err)
	}
	wantSilence(t, out)
}
