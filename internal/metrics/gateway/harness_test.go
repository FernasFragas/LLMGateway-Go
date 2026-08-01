package metrics

// Test harness: stub ports the decorators wrap, shared by every file in this
// package. Builders hide mechanics, never meaning — each test states one
// clause of the counting contract.

import (
	"context"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

var gptOpenAI = gateway.ModelProvider{Model: "gpt-4.1", Provider: "openai", Endpoint: "https://api.openai.com/v1"}

func chatRequest() gateway.ChatRequest {
	return gateway.ChatRequest{
		Model:     "gpt-4.1",
		Messages:  []gateway.Message{{Role: gateway.RoleUser, Content: "hello"}},
		MaxTokens: 512,
	}
}

type staticApps map[string]gateway.App

func (m staticApps) AppForKey(_ context.Context, key string) (gateway.App, bool) {
	app, ok := m[key]
	return app, ok
}

type limiter struct {
	decision gateway.RateDecision
	err      error
}

func (l limiter) Allow(context.Context, string) (gateway.RateDecision, error) {
	return l.decision, l.err
}

type slots struct {
	full    bool
	ceiling int
}

func (s slots) TryAcquire(string) (release func(), ceiling int, ok bool) {
	if s.full {
		return nil, s.ceiling, false
	}
	return func() {}, 0, true
}

type provider struct {
	completion gateway.Completion
	err        error
}

func (p provider) Complete(context.Context, gateway.ModelProvider, gateway.ChatRequest) (gateway.Completion, error) {
	return p.completion, p.err
}

// recordedUsage is the gateway.UsageRecorder the metrics decorator wraps; it
// remembers each call so tests can assert delegation reached it unchanged.
type recordedUsage struct {
	completions int
	rejections  int
}

func (r *recordedUsage) RecordCompletion(string, gateway.ModelProvider, gateway.Usage, time.Duration, bool) {
	r.completions++
}

func (r *recordedUsage) RecordRejection(string, gateway.ErrorCode) { r.rejections++ }

func (r *recordedUsage) RecordRateLimiterFailOpen(string) {}

func (r *recordedUsage) RecordDoubleSpendRisk(string, gateway.ModelProvider, int) {}

func (r *recordedUsage) RecordClientDisconnect(string, gateway.ModelProvider, int) {}
