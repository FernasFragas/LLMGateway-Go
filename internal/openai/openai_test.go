package openai

// The OpenAI translation contract, clause by clause: what the domain
// request looks like in /v1/chat/completions's dialect, what comes back in
// domain terms, and which failure earns which fault kind.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

const servedOpenAI = `{
	"choices": [{
		"message": {"role": "assistant", "content": "an answer"},
		"finish_reason": "stop"
	}],
	"usage": {"prompt_tokens": 21, "completion_tokens": 30, "total_tokens": 51}
}`

// sentRequest is the subset of the wire this suite inspects.
type sentRequest struct {
	Model               string   `json:"model"`
	MaxCompletionTokens int      `json:"max_completion_tokens"`
	MaxTokens           *int     `json:"max_tokens"` // must stay absent — see TestMaxTokensFieldIsNeverSent
	Temperature         float64  `json:"temperature"`
	TopP                float64  `json:"top_p"`
	Stop                []string `json:"stop"`
	Messages            []struct {
		Role       string `json:"role"`
		Content    string `json:"content"`
		ToolCallID string `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"messages"`
	Tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
	ToolChoice json.RawMessage `json:"tool_choice"`
}

func TestDomainRequestSpeaksOpenAI(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOpenAI)

	if _, err := complete(t, srv); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if rec.path != "/chat/completions" || rec.contentType != "application/json" {
		t.Errorf("sent %s %s, want application/json to /chat/completions", rec.contentType, rec.path)
	}
	if rec.auth != "Bearer sk-test-key" {
		t.Errorf("Authorization = %q, want a Bearer token", rec.auth)
	}

	var sent sentRequest
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("adapter sent unparseable JSON: %v", err)
	}

	if sent.Model != "gpt-4.1" || sent.MaxCompletionTokens != 512 {
		t.Errorf("model/max_completion_tokens = %q/%d, want the route's model and the request's max_tokens", sent.Model, sent.MaxCompletionTokens)
	}
	if sent.Temperature != 0.2 || sent.TopP != 0.9 || len(sent.Stop) != 1 {
		t.Errorf("sampling knobs = temp %v top_p %v stop %v, want the request's", sent.Temperature, sent.TopP, sent.Stop)
	}
	if string(sent.ToolChoice) != `"auto"` {
		t.Errorf("tool_choice = %s, want the bare string \"auto\"", sent.ToolChoice)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Type != "function" || sent.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("tools = %+v, want the one declared function", sent.Tools)
	}

	if len(sent.Messages) != 4 {
		t.Fatalf("messages = %d, want all four turns — no role remapping on this wire", len(sent.Messages))
	}
	if sent.Messages[0].Role != "system" || sent.Messages[3].Role != "tool" {
		t.Errorf("messages[0]/[3] roles = %q/%q, want system and tool unchanged", sent.Messages[0].Role, sent.Messages[3].Role)
	}
	if sent.Messages[3].ToolCallID != "call_1" {
		t.Errorf("tool_call_id = %q, want call_1 carried straight across", sent.Messages[3].ToolCallID)
	}
}

func TestMaxTokensFieldIsNeverSent(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOpenAI)

	_, _ = complete(t, srv)

	if strings.Contains(string(rec.body), `"max_tokens"`) {
		t.Errorf("max_tokens is deprecated on this endpoint; the adapter must send only max_completion_tokens:\n%s", rec.body)
	}
}

func TestToolCallArgumentsTravelAsAString(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOpenAI)

	_, _ = complete(t, srv)

	// Unlike Ollama and Anthropic, this wire already wants arguments as a
	// JSON-encoded string — the domain's own representation, verbatim.
	if !strings.Contains(string(rec.body), `"arguments":"{\"city\":\"Porto\"}"`) {
		t.Errorf("assistant tool_call arguments must be a JSON string on this wire:\n%s", rec.body)
	}
}

func TestToolChoiceNoneSendsTheWiresOwnNoneValue(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOpenAI)
	req := chatRequest()
	req.ToolChoice = &gateway.ToolChoice{Mode: gateway.ToolChoiceNone}

	if _, err := NewOpenAI(nil, "sk-test-key").Complete(context.Background(), modelProvider(srv), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var sent sentRequest
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("adapter sent unparseable JSON: %v", err)
	}

	// OpenAI has a real bare-string tool_choice "none" — the tools stay
	// declared (so tool_use blocks already in the transcript still match a
	// declared tool) and tool_choice tells the model not to call any of them.
	if len(sent.Tools) != 1 {
		t.Errorf("tools = %+v, want the declared tool still present under tool_choice none", sent.Tools)
	}
	if string(sent.ToolChoice) != `"none"` {
		t.Errorf("tool_choice = %s, want the bare string \"none\"", sent.ToolChoice)
	}
}

func TestToolChoiceFunctionForcesTheNamedTool(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOpenAI)
	req := chatRequest()
	req.ToolChoice = &gateway.ToolChoice{Mode: gateway.ToolChoiceFunction, Function: "get_weather"}

	if _, err := NewOpenAI(nil, "sk-test-key").Complete(context.Background(), modelProvider(srv), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var sent sentRequest
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("adapter sent unparseable JSON: %v", err)
	}

	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(sent.ToolChoice, &choice); err != nil {
		t.Fatalf("tool_choice not an object: %s", sent.ToolChoice)
	}
	if choice.Type != "function" || choice.Function.Name != "get_weather" {
		t.Errorf("tool_choice = %+v, want a forced function named get_weather", choice)
	}
}

func TestCompletionComesBackInDomainTerms(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, servedOpenAI)

	completion, err := complete(t, srv)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if completion.Message.Role != gateway.RoleAssistant || completion.Message.Content != "an answer" {
		t.Errorf("message = %+v, want the assistant's answer", completion.Message)
	}
	if completion.FinishReason != gateway.FinishStop {
		t.Errorf("finish = %q, want stop", completion.FinishReason)
	}
	if completion.Usage != (gateway.Usage{PromptTokens: 21, CompletionTokens: 30, TotalTokens: 51}) {
		t.Errorf("usage = %+v, want the response's token counts", completion.Usage)
	}
}

func TestLengthFinishIsReported(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `{
		"choices": [{"message": {"role": "assistant", "content": "an ans"}, "finish_reason": "length"}],
		"usage": {"prompt_tokens": 21, "completion_tokens": 512, "total_tokens": 533}
	}`)

	completion, err := complete(t, srv)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completion.FinishReason != gateway.FinishLength {
		t.Errorf("finish = %q, want length — the caller must know the answer hit its cap", completion.FinishReason)
	}
}

func TestToolCallsFinishReturnsTheProvidersOwnID(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `{
		"choices": [{
			"message": {"role": "assistant", "content": "",
				"tool_calls": [{"id": "call_abc", "function": {"name": "get_weather", "arguments": "{\"city\":\"Porto\"}"}}]},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 21, "completion_tokens": 9, "total_tokens": 30}
	}`)

	completion, err := complete(t, srv)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if completion.FinishReason != gateway.FinishToolCalls {
		t.Errorf("finish = %q, want tool_calls", completion.FinishReason)
	}
	calls := completion.Message.ToolCalls
	if len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("tool_calls = %+v, want the one call", calls)
	}
	if calls[0].ID != "call_abc" {
		t.Errorf("id = %q, want the provider's own id passed through — OpenAI mints one, unlike Ollama", calls[0].ID)
	}
	if calls[0].Arguments != `{"city":"Porto"}` {
		t.Errorf("arguments = %q, want the string's bytes verbatim as the domain's string", calls[0].Arguments)
	}
}

func TestContentFilterIsABadResponseNotAStop(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `{
		"choices": [{"message": {"role": "assistant", "content": ""}, "finish_reason": "content_filter"}],
		"usage": {"prompt_tokens": 21, "completion_tokens": 0, "total_tokens": 21}
	}`)

	_, err := complete(t, srv)

	fault := wantFault(t, err, gateway.FaultBadResponse)
	if fault.ObservedUsage == nil || fault.ObservedUsage.TotalTokens != 21 {
		t.Errorf("ObservedUsage = %+v, want the parsed counts even though the answer was filtered, not a real stop", fault.ObservedUsage)
	}
}

func TestNoChoicesIsABadResponse(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `{"choices": [], "usage": {"prompt_tokens": 5, "completion_tokens": 0, "total_tokens": 5}}`)

	_, err := complete(t, srv)

	wantFault(t, err, gateway.FaultBadResponse)
}

func TestRefusalsAreStatusFaults(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		kind   gateway.FaultKind
	}{
		"invalid api key is a server fault": {http.StatusUnauthorized, gateway.FaultServerError},
		"5xx is a server fault":             {http.StatusInternalServerError, gateway.FaultServerError},
		"429 keeps its own kind":            {http.StatusTooManyRequests, gateway.FaultThrottled},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := serving(t, tc.status, `{"error":{"message":"invalid_api_key","type":"invalid_request_error"}}`)

			_, err := complete(t, srv)

			fault := wantFault(t, err, tc.kind)
			if !strings.Contains(fault.Error(), "invalid_api_key") {
				t.Errorf("cause %q must carry the provider's text — for the log, and only the log", fault.Error())
			}
		})
	}
}

func TestPerTryDeadlineIsATimeoutFault(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, servedOpenAI)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := NewOpenAI(nil, "sk-test-key").Complete(ctx, modelProvider(srv), chatRequest())

	wantFault(t, err, gateway.FaultTimeout)
}

func TestDeadEndpointIsAnUnreachableFault(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, servedOpenAI)
	srv.Close() // the endpoint goes dark before the call

	_, err := complete(t, srv)

	wantFault(t, err, gateway.FaultUnreachable)
}

func TestGarbage200IsABadResponse(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `it works!`)

	_, err := complete(t, srv)

	fault := wantFault(t, err, gateway.FaultBadResponse)
	if fault.ObservedUsage != nil {
		t.Error("nothing was parseable — no usage to observe")
	}
}
