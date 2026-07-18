package main

// The composition contract: one request through newServer's full chain must
// land in every layer it promises — the log, the counters, and the wire —
// with no package knowing the others exist.

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/api"
	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/health"
	"github.com/FernasFragas/LLMGateway-Go/internal/metrics"
)

func TestOnePanickedRequestYieldsTheLogLineTheCountsAndThe500(t *testing.T) {
	srv, out, reqs, panics := composed(t, panickingCore{})

	rec := postChat(srv)

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal_error") {
		t.Errorf("caller got %d %q, want the standard 500 ErrorBody", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "the panic value") {
		t.Error("the panic value must never reach the caller")
	}
	for _, want := range []string{"handler panicked", "the panic value", "stack=", "request served", "status=500"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("log does not mention %q:\n%s", want, out.String())
		}
	}
	if panics.Panics() != 1 || reqs.Requests() != 1 || reqs.ServerErrors() != 1 {
		t.Errorf("counted panics=%d requests=%d 5xx=%d, want 1/1/1",
			panics.Panics(), reqs.Requests(), reqs.ServerErrors())
	}
}

func TestACalmRequestIsLoggedCountedAndServed(t *testing.T) {
	srv, out, reqs, panics := composed(t, servingCore{})

	rec := postChat(srv)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"request served", "POST", "/v1/chat", "status=200"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("log does not mention %q:\n%s", want, out.String())
		}
	}
	if reqs.Requests() != 1 || reqs.ServerErrors() != 0 || panics.Panics() != 0 {
		t.Errorf("counted requests=%d 5xx=%d panics=%d, want 1/0/0",
			reqs.Requests(), reqs.ServerErrors(), panics.Panics())
	}
}

// composed builds the server exactly as main will, with the log captured
// and the counters exposed.
func composed(t *testing.T, core api.ChatService) (*api.Server, *bytes.Buffer, *metrics.RequestMetrics, *metrics.PanicCounter) {
	t.Helper()
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reqs, panics := metrics.NewRequestMetrics(), metrics.NewPanicCounter()

	srv, err := newServer(api.Config{}, core, health.NewChecker(), log, reqs, panics)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return srv, buf, reqs, panics
}

// postChat sends a bearer-authenticated, schema-valid chat request through
// the full chain.
func postChat(srv *api.Server) *httptest.ResponseRecorder {
	body := `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-test-key")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// panickingCore stands in for a handler bug.
type panickingCore struct{}

func (panickingCore) Chat(context.Context, string, gateway.ChatRequest) (gateway.ChatResult, error) {
	panic("the panic value")
}

// servingCore answers every chat with a canned completion.
type servingCore struct{}

func (servingCore) Chat(context.Context, string, gateway.ChatRequest) (gateway.ChatResult, error) {
	return gateway.ChatResult{
		Model:        "gpt-4.1",
		Provider:     "openai",
		Message:      gateway.Message{Role: gateway.RoleAssistant, Content: "an answer"},
		FinishReason: gateway.FinishStop,
	}, nil
}
