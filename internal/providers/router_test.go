package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestTheRouteChoosesTheDialect(t *testing.T) {
	openai, anthropic := &recording{}, &recording{}
	r := router(t, map[string]gateway.ProviderClient{"openai": openai, "anthropic": anthropic}, nil)

	if _, err := r.Complete(context.Background(), route("anthropic"), gateway.ChatRequest{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if anthropic.calls != 1 || openai.calls != 0 {
		t.Errorf("calls: anthropic %d, openai %d — the route's provider picks the adapter", anthropic.calls, openai.calls)
	}
}

func TestAnUnwiredProviderFailsOverInsteadOfFailingTheCaller(t *testing.T) {
	// A route naming a provider nothing serves is a config mistake that
	// reached the request path. Unreachable costs nothing upstream, so the
	// core is free to try another route.
	r := router(t, map[string]gateway.ProviderClient{"openai": &recording{}}, nil)

	_, err := r.Complete(context.Background(), route("gemini"), gateway.ChatRequest{})

	var fault *gateway.ProviderFault
	if !errors.As(err, &fault) || fault.Kind != gateway.FaultUnreachable {
		t.Fatalf("error = %v, want an unreachable fault the core can fail over from", err)
	}
}

func TestARefusedCredentialTellsTheKeyCacheWhichProvider(t *testing.T) {
	// Decision #8: the rejection is evidence the cache is stale.
	var told []string
	refusing := &recording{err: &gateway.ProviderFault{Kind: gateway.FaultRejected}}
	r := router(t, map[string]gateway.ProviderClient{"openai": refusing},
		func(provider string) { told = append(told, provider) })

	_, err := r.Complete(context.Background(), route("openai"), gateway.ChatRequest{})

	if len(told) != 1 || told[0] != "openai" {
		t.Errorf("reported %v, want exactly the provider that refused", told)
	}
	var fault *gateway.ProviderFault
	if !errors.As(err, &fault) || fault.Kind != gateway.FaultRejected {
		t.Errorf("error = %v, want the fault passed through unchanged — the router observes, never handles", err)
	}
}

func TestOtherFailuresNeverTouchTheKeyCache(t *testing.T) {
	// A 5xx, a timeout, a garbage 200 — none of them are fixed by a new key,
	// and refreshing on each would put the secret store on the failure path
	// of every provider outage.
	for _, kind := range []gateway.FaultKind{
		gateway.FaultServerError, gateway.FaultThrottled,
		gateway.FaultTimeout, gateway.FaultBadResponse, gateway.FaultUnreachable,
	} {
		var told []string
		failing := &recording{err: &gateway.ProviderFault{Kind: kind}}
		r := router(t, map[string]gateway.ProviderClient{"openai": failing},
			func(provider string) { told = append(told, provider) })

		_, _ = r.Complete(context.Background(), route("openai"), gateway.ChatRequest{})

		if len(told) != 0 {
			t.Errorf("a %s fault scheduled a key refresh; only a rejected credential should", kind)
		}
	}
}

func TestSuccessNeverTouchesTheKeyCache(t *testing.T) {
	var told []string
	r := router(t, map[string]gateway.ProviderClient{"openai": &recording{}},
		func(provider string) { told = append(told, provider) })

	if _, err := r.Complete(context.Background(), route("openai"), gateway.ChatRequest{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(told) != 0 {
		t.Errorf("a served request scheduled a key refresh: %v", told)
	}
}

func TestAGatewayWithNoAdaptersIsAWiringError(t *testing.T) {
	if _, err := NewRouter(nil, nil); err == nil {
		t.Error("a router serving nothing must fail at boot, not per request")
	}
	if _, err := NewRouter(map[string]gateway.ProviderClient{"openai": nil}, nil); err == nil {
		t.Error("a nil adapter must fail at boot — otherwise it panics on the first request")
	}
}

// ─── harness ────────────────────────────────────────────────────────────────

// recording is a stub adapter counting calls and returning a fixed outcome.
type recording struct {
	calls int
	err   error
}

func (r *recording) Complete(context.Context, gateway.ModelProvider, gateway.ChatRequest) (gateway.Completion, error) {
	r.calls++
	if r.err != nil {
		return gateway.Completion{}, r.err
	}

	return gateway.Completion{Message: gateway.Message{Content: "an answer"}}, nil
}

func route(provider string) gateway.ModelProvider {
	return gateway.ModelProvider{Model: "a-model", Provider: provider, Endpoint: "https://example.invalid"}
}

func router(t *testing.T, adapters map[string]gateway.ProviderClient, onRejected Rejected) *Router {
	t.Helper()
	r, err := NewRouter(adapters, onRejected)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	return r
}
