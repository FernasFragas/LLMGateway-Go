package api

// The /v1/chat contract, clause by clause: what a completion looks like on
// the wire, which scope decisions refuse a request before the core sees it,
// and how each core failure code maps to a status. See openapi.yaml — these
// tests are that file, executable.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/health"
)

func TestChatCompletionOnTheWire(t *testing.T) {
	core := &chatStub{result: served()}

	rec := postChat(serving(t, core), validChatBody)

	body := wantChatResponse(t, rec)
	if !strings.HasPrefix(body.ID, "chatcmpl-") {
		t.Errorf("id = %q, want chatcmpl- prefix", body.ID)
	}
	if body.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", body.Object)
	}
	if body.Model != "gpt-4.1" {
		t.Errorf("model = %q, want the model that served", body.Model)
	}
	if len(body.Choices) != 1 || *body.Choices[0].Message.Content != "an answer" {
		t.Errorf("choices = %+v, want the served completion at index 0", body.Choices)
	}
	if body.Usage != (usageWire{PromptTokens: 21, CompletionTokens: 30, TotalTokens: 51}) {
		t.Errorf("usage = %+v, want the core's accounting", body.Usage)
	}
	if rec.Header().Get("X-Correlation-ID") == "" {
		t.Error("success responses must carry X-Correlation-ID too")
	}
}

func TestBearerKeyReachesTheCoreVerbatim(t *testing.T) {
	core := &chatStub{result: served()}

	postChat(serving(t, core), validChatBody)

	if core.gotKey != testKey {
		t.Errorf("core saw key %q, want the bearer token %q", core.gotKey, testKey)
	}
	if core.gotReq.Model != "gpt-4.1" || core.gotReq.MaxTokens != 512 {
		t.Errorf("core saw request %+v, want the decoded wire request", core.gotReq)
	}
}

func TestSubstitutionIsDisclosed(t *testing.T) {
	rec := postChat(serving(t, &chatStub{result: servedBySubstitute()}), validChatBody)

	body := wantChatResponse(t, rec)
	if rec.Header().Get("X-Model-Substituted") != "true" {
		t.Error("substitute served: X-Model-Substituted: true is mandatory")
	}
	if body.Model != "claude-sonnet-4" {
		t.Errorf("model = %q, want the substitute that served — never an echo", body.Model)
	}
}

func TestNoSubstitutionNoHeader(t *testing.T) {
	rec := postChat(serving(t, &chatStub{result: served()}), validChatBody)

	wantChatResponse(t, rec)
	if _, present := rec.Header()["X-Model-Substituted"]; present {
		t.Error("X-Model-Substituted must be absent when the requested model served")
	}
}

// --- scope decisions: refused at the edge, before the core ---

func TestUnknownParameterIsRejectedNotDropped(t *testing.T) {
	core := &chatStub{result: served()}
	body := `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"logprobs":true}`

	rec := postChat(serving(t, core), body)

	detail := wantErrorCode(t, rec, http.StatusBadRequest, "unsupported_parameter")
	if detail.Details == nil || detail.Details.Param != "logprobs" {
		t.Errorf("details = %+v, want the offending param named", detail.Details)
	}
	if core.calls != 0 {
		t.Error("a rejected request must never reach the core")
	}
}

func TestStreamTrueIsRefused(t *testing.T) {
	body := `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"stream":true}`

	rec := postChat(serving(t, &chatStub{}), body)

	detail := wantErrorCode(t, rec, http.StatusBadRequest, "unsupported_parameter")
	if detail.Details == nil || detail.Details.Param != "stream" {
		t.Errorf("details = %+v, want param stream", detail.Details)
	}
}

func TestStreamFalseIsAllowed(t *testing.T) {
	body := `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"stream":false}`

	rec := postChat(serving(t, &chatStub{result: served()}), body)

	wantChatResponse(t, rec)
}

func TestNAboveOneIsRefused(t *testing.T) {
	body := `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"n":2}`

	rec := postChat(serving(t, &chatStub{}), body)

	detail := wantErrorCode(t, rec, http.StatusBadRequest, "unsupported_parameter")
	if detail.Details == nil || detail.Details.Param != "n" {
		t.Errorf("details = %+v, want param n", detail.Details)
	}
}

func TestMultimodalContentIsRefused(t *testing.T) {
	body := `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`

	rec := postChat(serving(t, &chatStub{}), body)

	wantErrorCode(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestOutOfRangeSamplingParamsAreRefused(t *testing.T) {
	for name, body := range map[string]string{
		"temperature above 2": `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"temperature":2.5}`,
		"temperature below 0": `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"temperature":-0.1}`,
		"top_p above 1":       `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"top_p":1.1}`,
		"top_p below 0":       `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"top_p":-0.1}`,
	} {
		t.Run(name, func(t *testing.T) {
			core := &chatStub{result: served()}

			rec := postChat(serving(t, core), body)

			wantErrorCode(t, rec, http.StatusBadRequest, "invalid_request")
			if core.calls != 0 {
				t.Error("a rejected request must never reach the core")
			}
		})
	}
}

func TestBoundarySamplingParamsAreAllowed(t *testing.T) {
	body := `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"temperature":2,"top_p":0}`

	rec := postChat(serving(t, &chatStub{result: served()}), body)

	wantChatResponse(t, rec)
}

func TestOversizedBodyIsPayloadTooLarge(t *testing.T) {
	srv, err := New(Config{MaxBodyBytes: 64}, Deps{Chat: &chatStub{}, Health: health.NewChecker()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := postChat(srv, validChatBody)

	wantErrorCode(t, rec, http.StatusRequestEntityTooLarge, "payload_too_large")
}

func TestStopAcceptsStringOrArray(t *testing.T) {
	core := &chatStub{result: served()}
	body := `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"stop":"END"}`

	postChat(serving(t, core), body)

	if len(core.gotReq.Stop) != 1 || core.gotReq.Stop[0] != "END" {
		t.Errorf("stop = %v, want the single string as a one-element list", core.gotReq.Stop)
	}
}

func TestToolRoundTrip(t *testing.T) {
	core := &chatStub{result: gateway.ChatResult{
		Model:        "gpt-4.1",
		Message:      gateway.Message{Role: gateway.RoleAssistant, ToolCalls: []gateway.ToolCall{{ID: "call_1", Name: "get_weather", Arguments: `{"city":"Porto"}`}}},
		FinishReason: gateway.FinishToolCalls,
	}}
	body := `{"model":"gpt-4.1","max_tokens":512,
		"messages":[{"role":"user","content":"weather in Porto?"}],
		"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}],
		"tool_choice":"auto"}`

	rec := postChat(serving(t, core), body)

	if len(core.gotReq.Tools) != 1 || core.gotReq.Tools[0].Name != "get_weather" {
		t.Errorf("core saw tools %+v, want get_weather declared", core.gotReq.Tools)
	}
	if core.gotReq.ToolChoice == nil || core.gotReq.ToolChoice.Mode != gateway.ToolChoiceAuto {
		t.Errorf("core saw tool_choice %+v, want auto", core.gotReq.ToolChoice)
	}

	resp := wantChatResponse(t, rec)
	choice := resp.Choices[0]
	if choice.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 || choice.Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool_calls = %+v, want the model's call on the wire", choice.Message.ToolCalls)
	}
	if choice.Message.Content != nil {
		t.Error("content must be null on an assistant message carrying only tool calls")
	}
}

// --- core failures, code by code ---

func TestCoreFailuresMapToTheContract(t *testing.T) {
	cases := []struct {
		err    *gateway.Error
		status int
	}{
		{&gateway.Error{Code: gateway.CodeUnauthorized, Message: "missing or invalid API key"}, http.StatusUnauthorized},
		{&gateway.Error{Code: gateway.CodeInvalidRequest, Message: "model is required"}, http.StatusBadRequest},
		{&gateway.Error{Code: gateway.CodeUpstreamFailed, Message: "all eligible provider attempts failed"}, http.StatusBadGateway},
		{&gateway.Error{Code: gateway.CodeGatewayTimeout, Message: "total request deadline exhausted"}, http.StatusGatewayTimeout},
	}

	for _, tc := range cases {
		t.Run(string(tc.err.Code), func(t *testing.T) {
			rec := postChat(serving(t, &chatStub{err: tc.err}), validChatBody)

			wantErrorCode(t, rec, tc.status, string(tc.err.Code))
		})
	}
}

func TestQuotaExceededCarriesRetryAfterAndTheWindow(t *testing.T) {
	core := &chatStub{err: &gateway.Error{
		Code:       gateway.CodeQuotaExceeded,
		Message:    "agent-service is over its rate quota",
		RetryAfter: 1500 * time.Millisecond,
		Quota:      &gateway.QuotaDetail{Limit: 100, WindowSeconds: 60, Used: 100},
	}}

	rec := postChat(serving(t, core), validChatBody)

	detail := wantErrorCode(t, rec, http.StatusTooManyRequests, "quota_exceeded")
	if rec.Header().Get("Retry-After") != "2" {
		t.Errorf("Retry-After = %q, want 2 — rounded up, never early", rec.Header().Get("Retry-After"))
	}
	if detail.Details == nil || detail.Details.Quota == nil || detail.Details.Quota.Limit != 100 {
		t.Errorf("details = %+v, want the refusing window disclosed", detail.Details)
	}
}

func TestConcurrencyCeilingCarriesRetryAfterAndTheCeiling(t *testing.T) {
	core := &chatStub{err: &gateway.Error{
		Code:        gateway.CodeConcurrencyCeiling,
		Message:     "agent-service is at its in-flight ceiling (300)",
		RetryAfter:  time.Second,
		MaxInFlight: 300,
	}}

	rec := postChat(serving(t, core), validChatBody)

	detail := wantErrorCode(t, rec, http.StatusTooManyRequests, "concurrency_ceiling")
	if rec.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want 1 — mandatory on every 429", rec.Header().Get("Retry-After"))
	}
	if detail.Details == nil || detail.Details.MaxInFlight != 300 {
		t.Errorf("details = %+v, want max_in_flight 300", detail.Details)
	}
}

func TestModelUnavailableNamesTheRequestedModel(t *testing.T) {
	core := &chatStub{err: &gateway.Error{
		Code:           gateway.CodeModelUnavailable,
		Message:        "no eligible model provider for gpt-4.1 under the app's failover policy",
		RequestedModel: "gpt-4.1",
	}}

	rec := postChat(serving(t, core), validChatBody)

	detail := wantErrorCode(t, rec, http.StatusServiceUnavailable, "model_unavailable")
	if detail.Details == nil || detail.Details.RequestedModel != "gpt-4.1" {
		t.Errorf("details = %+v, want requested_model gpt-4.1", detail.Details)
	}
}

func TestCallerDisconnectGetsNoBody(t *testing.T) {
	core := &chatStub{err: fmt.Errorf("caller disconnected: %w", context.Canceled)}

	rec := postChat(serving(t, core), validChatBody)

	wantStatus(t, rec, statusClientClosedRequest)
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want nothing — there is no one to answer", rec.Body.String())
	}
}

// --- probes ---

func TestHealthzReportsTheProcess(t *testing.T) {
	rec := get(serving(t, &chatStub{}), "/healthz")

	wantStatus(t, rec, http.StatusOK)
}

func TestReadyzFailsClosedUntilConditionsHold(t *testing.T) {
	checker := health.NewChecker()
	warm := false
	checker.AddReadiness("key-cache", func(context.Context) error {
		if !warm {
			return fmt.Errorf("no keys loaded")
		}
		return nil
	})
	srv := servingWith(t, &chatStub{}, checker)

	wantStatus(t, get(srv, "/readyz"), http.StatusServiceUnavailable)

	warm = true
	wantStatus(t, get(srv, "/readyz"), http.StatusOK)
}
