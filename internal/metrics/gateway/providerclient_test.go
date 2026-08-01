package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestServedAttemptCountsWithoutAFailure(t *testing.T) {
	pc := NewProviderClient(provider{completion: gateway.Completion{Usage: gateway.Usage{TotalTokens: 51}}})

	_, err := pc.Complete(context.Background(), gptOpenAI, chatRequest())

	if err != nil {
		t.Fatal(err)
	}
	if pc.Attempts() != 1 {
		t.Errorf("Attempts() = %d, want 1", pc.Attempts())
	}
	if pc.Failures() != 0 {
		t.Errorf("Failures() = %d, want 0 — a served attempt is not a failure", pc.Failures())
	}
}

func TestFailedAttemptCountsByKindAndPassesTheErrorThrough(t *testing.T) {
	fault := &gateway.ProviderFault{Kind: gateway.FaultServerError, Cause: errors.New("openai said: 500")}
	pc := NewProviderClient(provider{err: fault})

	_, err := pc.Complete(context.Background(), gptOpenAI, chatRequest())

	if !errors.Is(err, fault) {
		t.Errorf("error = %v, want the fault unchanged — the decorator observes, never handles", err)
	}
	if pc.Attempts() != 1 {
		t.Errorf("Attempts() = %d, want 1", pc.Attempts())
	}
	if pc.Failures() != 1 {
		t.Errorf("Failures() = %d, want 1", pc.Failures())
	}
	if got := pc.FailuresByKind(gateway.FaultServerError); got != 1 {
		t.Errorf("FailuresByKind(server_error) = %d, want 1", got)
	}
	if got := pc.FailuresByKind(gateway.FaultTimeout); got != 0 {
		t.Errorf("FailuresByKind(timeout) = %d, want 0 — the wrong kind must not bleed into another's count", got)
	}
}

func TestUnclassifiedErrorStillCountsAsAFailure(t *testing.T) {
	pc := NewProviderClient(provider{err: errors.New("bare error, not a ProviderFault")})

	_, _ = pc.Complete(context.Background(), gptOpenAI, chatRequest())

	if pc.Failures() != 1 {
		t.Errorf("Failures() = %d, want 1 even for an error the core never wrapped", pc.Failures())
	}
	if got := pc.FailuresByKind("unclassified"); got != 1 {
		t.Errorf("FailuresByKind(unclassified) = %d, want 1", got)
	}
}
