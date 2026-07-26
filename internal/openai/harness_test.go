package openai

// Test harness: a provider endpoint in miniature that records what the
// adapter sent, domain builders, and fault assertions. Builders hide
// mechanics, never meaning — each test states one clause of the
// translation contract.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// chatRequest exercises every translatable field: sampling knobs, stop
// sequences, declared tools, and a full tool round-trip.
func chatRequest() gateway.ChatRequest {
	temp, topP := 0.2, 0.9

	return gateway.ChatRequest{
		Model:       "gpt-4.1",
		MaxTokens:   512,
		Temperature: &temp,
		TopP:        &topP,
		Stop:        []string{"END"},
		Messages: []gateway.Message{
			{Role: gateway.RoleSystem, Content: "be brief"},
			{Role: gateway.RoleUser, Content: "weather in Porto?"},
			{Role: gateway.RoleAssistant, ToolCalls: []gateway.ToolCall{{
				ID: "call_1", Name: "get_weather", Arguments: `{"city":"Porto"}`,
			}}},
			{Role: gateway.RoleTool, ToolCallID: "call_1", Content: `{"celsius":19}`},
		},
		Tools: []gateway.Tool{{
			Name:        "get_weather",
			Description: "current weather for a city",
			Parameters:  []byte(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
		ToolChoice: &gateway.ToolChoice{Mode: gateway.ToolChoiceAuto},
	}
}

// recorded is what the adapter actually put on the wire.
type recorded struct {
	path        string
	contentType string
	auth        string
	body        []byte
}

// serving answers every request with the given status and body, recording
// the last request for the test to judge.
func serving(t *testing.T, status int, response string) (*httptest.Server, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.URL.Path
		rec.contentType = r.Header.Get("Content-Type")
		rec.auth = r.Header.Get("Authorization")
		rec.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// modelProvider points the adapter at the test server.
func modelProvider(srv *httptest.Server) gateway.ModelProvider {
	return gateway.ModelProvider{Model: "gpt-4.1", Provider: "openai", Endpoint: srv.URL}
}

// wantFault asserts err is a ProviderFault of the given kind and returns it
// for kind-specific clauses.
func wantFault(t *testing.T, err error, kind gateway.FaultKind) *gateway.ProviderFault {
	t.Helper()
	var fault *gateway.ProviderFault
	if !errors.As(err, &fault) {
		t.Fatalf("error %v is not a *gateway.ProviderFault — the core cannot classify it", err)
	}
	if fault.Kind != kind {
		t.Fatalf("fault kind = %q, want %q; cause: %v", fault.Kind, kind, fault.Cause)
	}
	return fault
}

func complete(t *testing.T, srv *httptest.Server) (gateway.Completion, error) {
	t.Helper()
	return NewOpenAI(nil, "sk-test-key").Complete(context.Background(), modelProvider(srv), chatRequest())
}
