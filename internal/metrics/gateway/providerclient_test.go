package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// configuredProviders is the closed set these tests build against — the same
// shape config supplies at boot.
var configuredProviders = []string{"openai", "anthropic"}

func TestServedAttemptCountsWithoutAFailure(t *testing.T) {
	pc := NewProviderClient(provider{completion: gateway.Completion{Usage: gateway.Usage{TotalTokens: 51}}}, configuredProviders)

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

func TestFailedAttemptCountsByProviderAndKindAndPassesTheErrorThrough(t *testing.T) {
	fault := &gateway.ProviderFault{Kind: gateway.FaultServerError, Cause: errors.New("openai said: 500")}
	pc := NewProviderClient(provider{err: fault}, configuredProviders)

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
	if got := pc.FailuresBy("openai", gateway.FaultServerError); got != 1 {
		t.Errorf("FailuresBy(openai, server_error) = %d, want 1", got)
	}
	if got := pc.FailuresBy("openai", gateway.FaultTimeout); got != 0 {
		t.Errorf("FailuresBy(openai, timeout) = %d, want 0 — the wrong kind must not bleed into another's count", got)
	}
}

func TestOneProvidersFailuresAreNeverAnothersFailures(t *testing.T) {
	// The whole point of the dimension: "upstream failures are up" is not
	// actionable, "anthropic's timeouts are up" is.
	pc := NewProviderClient(provider{err: &gateway.ProviderFault{Kind: gateway.FaultTimeout}}, configuredProviders)

	_, _ = pc.Complete(context.Background(), gptOpenAI, chatRequest())

	if got := pc.AttemptsByProvider("anthropic"); got != 0 {
		t.Errorf("AttemptsByProvider(anthropic) = %d, want 0 — openai's attempt was attributed to anthropic", got)
	}
	if got := pc.FailuresBy("anthropic", gateway.FaultTimeout); got != 0 {
		t.Errorf("FailuresBy(anthropic, timeout) = %d, want 0", got)
	}
	if got := pc.AttemptsByProvider("openai"); got != 1 {
		t.Errorf("AttemptsByProvider(openai) = %d, want 1", got)
	}
}

func TestUnclassifiedErrorStillCountsAsAFailure(t *testing.T) {
	pc := NewProviderClient(provider{err: errors.New("bare error, not a ProviderFault")}, configuredProviders)

	_, _ = pc.Complete(context.Background(), gptOpenAI, chatRequest())

	if pc.Failures() != 1 {
		t.Errorf("Failures() = %d, want 1 even for an error the core never wrapped", pc.Failures())
	}
	if got := pc.FailuresBy("openai", KindUnclassified); got != 1 {
		t.Errorf("FailuresBy(openai, unclassified) = %d, want 1", got)
	}
}

func TestAnUnconfiguredProviderIsDroppedRatherThanCreatingASeries(t *testing.T) {
	// The cardinality bound is structural: a provider outside the configured
	// set cannot mint a counter, whatever reaches the decorator. The total
	// stays honest about what it can attribute — which is nothing here.
	pc := NewProviderClient(provider{}, configuredProviders)
	unknown := gateway.ModelProvider{Model: "llama3", Provider: "ollama"}

	_, _ = pc.Complete(context.Background(), unknown, chatRequest())

	if got := pc.AttemptsByProvider("ollama"); got != 0 {
		t.Errorf("AttemptsByProvider(ollama) = %d, want 0 — an unconfigured provider must not create a series", got)
	}
	if len(pc.Providers()) != len(configuredProviders) {
		t.Errorf("Providers() = %v, want only the configured set", pc.Providers())
	}
}
