package logs

import (
	"context"
	"errors"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestATokenLimiterOutageIsLoggedAndTheErrorStillReturned(t *testing.T) {
	log, out := captured(t)
	outage := errors.New("redis: connection refused")
	lim := NewTokenLimiter(tokenLimiter{checkErr: outage}, log)

	_, err := lim.Check(context.Background(), "rag-api")

	if !errors.Is(err, outage) {
		t.Errorf("error = %v, want the limiter's own, unchanged — the decorator observes, never handles", err)
	}
	wantLogged(t, out, "budget unenforced", "rag-api", "redis: connection refused")
}

func TestALostDebitIsLoggedWithWhatWasLost(t *testing.T) {
	// Nobody is left to tell: the caller already has a real answer. The log is
	// the only place this can surface, so it must carry the size of the gap.
	log, out := captured(t)
	lim := NewTokenLimiter(tokenLimiter{settleErr: errors.New("redis: connection refused")}, log)

	if err := lim.Settle(context.Background(), "rag-api", 51); err == nil {
		t.Fatal("error = nil, want the limiter's own returned so the metrics decorator counts it")
	}

	wantLogged(t, out, "under-counted", "rag-api", "51")
}

func TestBudgetLeftPassesThroughSilently(t *testing.T) {
	log, out := captured(t)
	lim := NewTokenLimiter(tokenLimiter{decision: gateway.RateDecision{Allowed: true}}, log)

	decision, err := lim.Check(context.Background(), "rag-api")

	if err != nil || !decision.Allowed {
		t.Fatalf("decision = %+v (err=%v), want allowed passed through unchanged", decision, err)
	}
	wantSilence(t, out)
}

func TestASuccessfulDebitPassesThroughSilently(t *testing.T) {
	log, out := captured(t)
	lim := NewTokenLimiter(tokenLimiter{}, log)

	if err := lim.Settle(context.Background(), "rag-api", 51); err != nil {
		t.Fatal(err)
	}

	wantSilence(t, out)
}
