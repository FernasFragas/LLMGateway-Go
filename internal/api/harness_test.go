package api

// Test harness: a stubbed core, one-line request senders, and assertions
// that read the wire. Builders hide mechanics, never meaning — each test
// states one clause of the HTTP contract in openapi.yaml.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/health"
)

const testKey = "sk-test-secret-key"

const validChatBody = `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"the caller's private prompt"}]}`

// chatStub is the core behind the edge: it returns a canned outcome and
// remembers what the adapter handed it.
type chatStub struct {
	result gateway.ChatResult
	err    error

	calls  int
	gotKey string
	gotReq gateway.ChatRequest
}

func (c *chatStub) Chat(_ context.Context, apiKey string, req gateway.ChatRequest) (gateway.ChatResult, error) {
	c.calls++
	c.gotKey = apiKey
	c.gotReq = req
	return c.result, c.err
}

// served is a completion the core produced on the primary route.
func served() gateway.ChatResult {
	return gateway.ChatResult{
		Model:        "gpt-4.1",
		Provider:     "openai",
		Message:      gateway.Message{Role: gateway.RoleAssistant, Content: "an answer"},
		FinishReason: gateway.FinishStop,
		Usage:        gateway.Usage{PromptTokens: 21, CompletionTokens: 30, TotalTokens: 51},
	}
}

// servedBySubstitute is a completion a policy-permitted substitute served.
func servedBySubstitute() gateway.ChatResult {
	res := served()
	res.Model = "claude-sonnet-4"
	res.Provider = "anthropic"
	res.Substituted = true
	return res
}

// serving builds a Server over the given core stub.
func serving(t *testing.T, chat gateway.ChatService) *Server {
	t.Helper()
	return servingWith(t, chat, health.NewChecker())
}

func servingWith(t *testing.T, chat gateway.ChatService, checker *health.Checker) *Server {
	t.Helper()
	srv, err := New(Config{}, Deps{Chat: chat, Health: checker})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// servingObserved is serving with the given decorators spliced into the two
// seams — how tests state where routes() places each seam.
func servingObserved(t *testing.T, chat gateway.ChatService, reqMW, panicMW []Middleware) *Server {
	t.Helper()
	srv, err := New(Config{}, Deps{
		Chat: chat, Health: health.NewChecker(),
		RequestMiddleware: reqMW, PanicMiddleware: panicMW,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// postChat sends a bearer-authenticated chat request through the full chain.
func postChat(srv *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testKey)
	return do(srv, req)
}

func get(srv *Server, path string) *httptest.ResponseRecorder {
	return do(srv, httptest.NewRequest(http.MethodGet, path, nil))
}

func do(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// --- assertions ---

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, want, rec.Body.String())
	}
}

// wantErrorCode asserts the ErrorBody contract — the code, a non-empty
// message, and a correlation_id matching the header — and returns the
// decoded detail for code-specific clauses.
func wantErrorCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errorDetail {
	t.Helper()
	wantStatus(t, rec, status)

	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v\n%s", err, rec.Body.String())
	}
	if body.Error.Code != code {
		t.Errorf("error.code = %q, want %q", body.Error.Code, code)
	}
	if body.Error.Message == "" {
		t.Error("error.message is empty")
	}
	if body.Error.CorrelationID == "" {
		t.Error("error.correlation_id is empty")
	}
	if header := rec.Header().Get("X-Correlation-ID"); header != body.Error.CorrelationID {
		t.Errorf("X-Correlation-ID header %q != body correlation_id %q", header, body.Error.CorrelationID)
	}
	return body.Error
}

func wantChatResponse(t *testing.T, rec *httptest.ResponseRecorder) chatResponseWire {
	t.Helper()
	wantStatus(t, rec, http.StatusOK)

	var body chatResponseWire
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
	return body
}
