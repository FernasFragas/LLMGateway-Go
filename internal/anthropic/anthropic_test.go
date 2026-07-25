package anthropic

// The Anthropic translation contract, clause by clause: what the domain
// request looks like in /v1/messages's dialect, what comes back in domain
// terms, and which failure earns which fault kind.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

const servedAnthropic = `{
	"id": "msg_1", "type": "message", "role": "assistant",
	"content": [{"type": "text", "text": "an answer"}],
	"stop_reason": "end_turn",
	"usage": {"input_tokens": 21, "output_tokens": 30}
}`

// sentRequest is the subset of the wire this suite inspects.
type sentRequest struct {
	Model         string   `json:"model"`
	MaxTokens     int      `json:"max_tokens"`
	System        string   `json:"system"`
	Temperature   float64  `json:"temperature"`
	TopP          float64  `json:"top_p"`
	StopSequences []string `json:"stop_sequences"`
	Messages      []struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   string          `json:"content"`
		} `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	} `json:"tools"`
	ToolChoice *struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"tool_choice"`
}

func TestDomainRequestSpeaksAnthropic(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedAnthropic)

	if _, err := complete(t, srv); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if rec.path != "/messages" || rec.contentType != "application/json" {
		t.Errorf("sent %s %s, want application/json to /messages", rec.contentType, rec.path)
	}
	if rec.apiKey != "sk-ant-test" || rec.version != anthropicVersion {
		t.Errorf("headers x-api-key=%q anthropic-version=%q, want the configured key and the pinned version", rec.apiKey, rec.version)
	}

	var sent sentRequest
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("adapter sent unparseable JSON: %v", err)
	}

	if sent.Model != "claude-sonnet-4" || sent.MaxTokens != 512 {
		t.Errorf("model/max_tokens = %q/%d, want the route's model and the request's max_tokens", sent.Model, sent.MaxTokens)
	}
	if sent.System != "be brief" {
		t.Errorf("system = %q, want the system turn pulled out of messages entirely", sent.System)
	}
	if sent.Temperature != 0.2 || sent.TopP != 0.9 || len(sent.StopSequences) != 1 {
		t.Errorf("sampling knobs = temp %v top_p %v stop %v, want the request's", sent.Temperature, sent.TopP, sent.StopSequences)
	}
	if sent.ToolChoice == nil || sent.ToolChoice.Type != "auto" {
		t.Errorf("tool_choice = %+v, want {type: auto}", sent.ToolChoice)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Name != "get_weather" {
		t.Fatalf("tools = %+v, want the one declared function", sent.Tools)
	}

	if len(sent.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 — the system turn must not appear here", len(sent.Messages))
	}
	if sent.Messages[0].Role != "user" || sent.Messages[0].Content[0].Type != "text" {
		t.Errorf("message[0] = %+v, want the user's text turn", sent.Messages[0])
	}
	if sent.Messages[1].Role != "assistant" || sent.Messages[1].Content[0].Type != "tool_use" {
		t.Errorf("message[1] = %+v, want the assistant's tool_use turn", sent.Messages[1])
	}
}

func TestSystemMessageNeverAppearsInMessages(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedAnthropic)

	_, _ = complete(t, srv)

	var sent sentRequest
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("adapter sent unparseable JSON: %v", err)
	}
	for _, m := range sent.Messages {
		if m.Role == "system" {
			t.Fatalf("a \"system\" role reached messages — Anthropic rejects this; the system turn must be the top-level field")
		}
	}
}

func TestToolCallArgumentsTravelAsAnObject(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedAnthropic)

	_, _ = complete(t, srv)

	// The domain carries arguments as a verbatim string; this wire wants the
	// object itself under "input". `"input":"{\"city\"...}"` (a string)
	// would be the bug.
	if !strings.Contains(string(rec.body), `"input":{"city":"Porto"}`) {
		t.Errorf("assistant tool_use input must be a JSON object on this wire:\n%s", rec.body)
	}
}

func TestToolResultTravelsAsAUserMessage(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedAnthropic)

	_, _ = complete(t, srv)

	var sent sentRequest
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("adapter sent unparseable JSON: %v", err)
	}

	last := sent.Messages[len(sent.Messages)-1]
	if last.Role != "user" || len(last.Content) != 1 || last.Content[0].Type != "tool_result" {
		t.Fatalf("last message = %+v, want a user message holding one tool_result block — Anthropic has no tool role", last)
	}
	if last.Content[0].ToolUseID != "call_1" || last.Content[0].Content != `{"celsius":19}` {
		t.Errorf("tool_result = %+v, want it naming call_1 and carrying the domain's content verbatim", last.Content[0])
	}
}

func TestToolChoiceNoneSendsTheWiresOwnNoneValue(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedAnthropic)
	req := chatRequest()
	req.ToolChoice = &gateway.ToolChoice{Mode: gateway.ToolChoiceNone}

	if _, err := NewAnthropic(nil, "sk-ant-test").Complete(context.Background(), modelProvider(srv), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var sent sentRequest
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("adapter sent unparseable JSON: %v", err)
	}

	// Anthropic has a real tool_choice "none" — the tools stay declared (so
	// tool_use blocks already in the transcript still match a declared tool)
	// and tool_choice tells the model not to call any of them.
	if len(sent.Tools) != 1 {
		t.Errorf("tools = %+v, want the declared tool still present under tool_choice none", sent.Tools)
	}
	if sent.ToolChoice == nil || sent.ToolChoice.Type != "none" {
		t.Errorf("tool_choice = %+v, want {type: none}", sent.ToolChoice)
	}
}

func TestToolChoiceFunctionForcesTheNamedTool(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedAnthropic)
	req := chatRequest()
	req.ToolChoice = &gateway.ToolChoice{Mode: gateway.ToolChoiceFunction, Function: "get_weather"}

	if _, err := NewAnthropic(nil, "sk-ant-test").Complete(context.Background(), modelProvider(srv), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var sent sentRequest
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("adapter sent unparseable JSON: %v", err)
	}
	if sent.ToolChoice == nil || sent.ToolChoice.Type != "tool" || sent.ToolChoice.Name != "get_weather" {
		t.Errorf("tool_choice = %+v, want {type: tool, name: get_weather}", sent.ToolChoice)
	}
}

func TestCompletionComesBackInDomainTerms(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, servedAnthropic)

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
		t.Errorf("usage = %+v, want input/output tokens summed into the domain's accounting", completion.Usage)
	}
}

func TestMaxTokensStopIsReportedAsLength(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `{
		"content": [{"type": "text", "text": "an ans"}],
		"stop_reason": "max_tokens",
		"usage": {"input_tokens": 21, "output_tokens": 512}
	}`)

	completion, err := complete(t, srv)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completion.FinishReason != gateway.FinishLength {
		t.Errorf("finish = %q, want length — the caller must know the answer hit its cap", completion.FinishReason)
	}
}

func TestToolUseStopReturnsToolCallsWithTheProvidersOwnID(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `{
		"content": [{"type": "tool_use", "id": "toolu_abc", "name": "get_weather", "input": {"city":"Porto"}}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 21, "output_tokens": 9}
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
	if calls[0].ID != "toolu_abc" {
		t.Errorf("id = %q, want the provider's own tool_use id passed through — Anthropic mints one, unlike Ollama", calls[0].ID)
	}
	if calls[0].Arguments != `{"city":"Porto"}` {
		t.Errorf("arguments = %q, want the input object's bytes verbatim as the domain's string", calls[0].Arguments)
	}
}

func TestUnrecognizedStopReasonIsABadResponse(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `{
		"content": [{"type": "text", "text": "partial"}],
		"stop_reason": "pause_turn",
		"usage": {"input_tokens": 21, "output_tokens": 4}
	}`)

	_, err := complete(t, srv)

	fault := wantFault(t, err, gateway.FaultBadResponse)
	if fault.ObservedUsage == nil || fault.ObservedUsage.TotalTokens != 25 {
		t.Errorf("ObservedUsage = %+v, want the parsed counts even though the stop_reason wasn't trusted", fault.ObservedUsage)
	}
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
			srv, _ := serving(t, tc.status, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)

			_, err := complete(t, srv)

			fault := wantFault(t, err, tc.kind)
			if !strings.Contains(fault.Error(), "invalid x-api-key") {
				t.Errorf("cause %q must carry the provider's text — for the log, and only the log", fault.Error())
			}
		})
	}
}

func TestPerTryDeadlineIsATimeoutFault(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, servedAnthropic)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := NewAnthropic(nil, "sk-ant-test").Complete(ctx, modelProvider(srv), chatRequest())

	wantFault(t, err, gateway.FaultTimeout)
}

func TestDeadEndpointIsAnUnreachableFault(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, servedAnthropic)
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
