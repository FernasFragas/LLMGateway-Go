package ollama

// The Ollama translation contract, clause by clause: what the domain
// request looks like in /api/chat's dialect, what comes back in domain
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

const servedOllama = `{
	"message": {"role": "assistant", "content": "an answer"},
	"done": true, "done_reason": "stop",
	"prompt_eval_count": 21, "eval_count": 30
}`

func TestDomainRequestSpeaksOllama(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOllama)

	if _, err := complete(t, srv); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if rec.path != "/api/chat" || rec.contentType != "application/json" {
		t.Errorf("sent %s %s, want application/json to /api/chat", rec.contentType, rec.path)
	}

	var sent struct {
		Model    string           `json:"model"`
		Stream   *bool            `json:"stream"`
		Messages []map[string]any `json:"messages"`
		Tools    []map[string]any `json:"tools"`
		Options  struct {
			Temperature float64  `json:"temperature"`
			TopP        float64  `json:"top_p"`
			Stop        []string `json:"stop"`
			NumPredict  int      `json:"num_predict"`
		} `json:"options"`
	}
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("adapter sent unparseable JSON: %v", err)
	}

	if sent.Model != "llama3" {
		t.Errorf("model = %q, want the route's model", sent.Model)
	}
	if sent.Stream == nil || *sent.Stream {
		t.Error("stream must be present and false — Ollama streams by default, and streaming is refused by decision")
	}
	if sent.Options.NumPredict != 512 {
		t.Errorf("options.num_predict = %d, want max_tokens (512) under the wire's name", sent.Options.NumPredict)
	}
	if sent.Options.Temperature != 0.2 || sent.Options.TopP != 0.9 || len(sent.Options.Stop) != 1 {
		t.Errorf("options = %+v, want the request's sampling knobs", sent.Options)
	}
	if len(sent.Messages) != 4 || sent.Messages[0]["role"] != "system" || sent.Messages[3]["role"] != "tool" {
		t.Errorf("messages = %v, want all four turns in order", sent.Messages)
	}
	if len(sent.Tools) != 1 {
		t.Fatalf("tools = %v, want the one declared function", sent.Tools)
	}
}

func TestSelfHostedSendsNoAuthorizationHeader(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOllama)

	// Empty key = self-hosted, in-cluster instance: no credential to send.
	if _, err := NewOllama(nil, "").Complete(context.Background(), modelProvider(srv), chatRequest()); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if rec.auth != "" {
		t.Errorf("Authorization = %q, want none — a self-hosted endpoint needs no credential", rec.auth)
	}
}

func TestCloudKeyRidesAsABearerToken(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOllama)

	// A configured key = Ollama Cloud: Bearer on every request.
	if _, err := NewOllama(nil, "sk-ollama-test").Complete(context.Background(), modelProvider(srv), chatRequest()); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if rec.auth != "Bearer sk-ollama-test" {
		t.Errorf("Authorization = %q, want the configured key as a Bearer token", rec.auth)
	}
}

func TestToolCallArgumentsTravelAsAnObject(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOllama)

	_, _ = complete(t, srv)

	// The domain carries arguments as a verbatim string; this wire wants
	// the object itself. `"{\"city\"...}"` (a string) would be the bug.
	if !strings.Contains(string(rec.body), `"arguments":{"city":"Porto"}`) {
		t.Errorf("assistant tool_call arguments must be a JSON object on this wire:\n%s", rec.body)
	}
}

func TestToolResultNamesTheToolItAnswers(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOllama)

	if _, err := complete(t, srv); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var sent struct {
		Messages []struct {
			Role     string `json:"role"`
			ToolName string `json:"tool_name"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("adapter sent unparseable JSON: %v", err)
	}

	// The domain's tool message carries only the call id (call_1); Ollama
	// matches results to calls by name, so the adapter must resolve the id
	// back to get_weather from the earlier assistant tool_call.
	tool := sent.Messages[len(sent.Messages)-1]
	if tool.Role != "tool" || tool.ToolName != "get_weather" {
		t.Errorf("last message = %+v, want the tool result naming get_weather", tool)
	}
}

func TestToolChoiceNoneDeclaresNoTools(t *testing.T) {
	srv, rec := serving(t, http.StatusOK, servedOllama)
	req := chatRequest()
	req.ToolChoice = &gateway.ToolChoice{Mode: gateway.ToolChoiceNone}

	if _, err := NewOllama(nil, "").Complete(context.Background(), modelProvider(srv), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Ollama has no tool_choice; "none" is honored by declaring nothing.
	if strings.Contains(string(rec.body), `"tools"`) {
		t.Errorf("tool_choice none must suppress the tools declaration:\n%s", rec.body)
	}
}

func TestCompletionComesBackInDomainTerms(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, servedOllama)

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
		t.Errorf("usage = %+v, want eval counts summed into the domain's accounting", completion.Usage)
	}
}

func TestLengthStopIsReported(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `{
		"message": {"role": "assistant", "content": "an ans"},
		"done": true, "done_reason": "length",
		"prompt_eval_count": 21, "eval_count": 512
	}`)

	completion, err := complete(t, srv)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completion.FinishReason != gateway.FinishLength {
		t.Errorf("finish = %q, want length — the caller must know the answer hit its cap", completion.FinishReason)
	}
}

func TestToolCallsComeBackWithMintedIDs(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `{
		"message": {"role": "assistant", "content": "",
			"tool_calls": [{"function": {"name": "get_weather", "arguments": {"city":"Porto"}}}]},
		"done": true, "done_reason": "stop",
		"prompt_eval_count": 21, "eval_count": 9
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
	if calls[0].ID == "" {
		t.Error("the contract requires an id; Ollama sends none, so the adapter must mint one")
	}
	if calls[0].Arguments != `{"city":"Porto"}` {
		t.Errorf("arguments = %q, want the object's bytes verbatim as the domain's string", calls[0].Arguments)
	}
}

func TestRefusalsAreStatusFaults(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		kind   gateway.FaultKind
	}{
		"model not found is a server fault": {http.StatusNotFound, gateway.FaultServerError},
		"5xx is a server fault":             {http.StatusInternalServerError, gateway.FaultServerError},
		"429 keeps its own kind":            {http.StatusTooManyRequests, gateway.FaultThrottled},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := serving(t, tc.status, `{"error":"model 'llama3' not found"}`)

			_, err := complete(t, srv)

			fault := wantFault(t, err, tc.kind)
			if !strings.Contains(fault.Error(), "not found") {
				t.Errorf("cause %q must carry the provider's text — for the log, and only the log", fault.Error())
			}
		})
	}
}

func TestPerTryDeadlineIsATimeoutFault(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, servedOllama)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := NewOllama(nil, "").Complete(ctx, modelProvider(srv), chatRequest())

	wantFault(t, err, gateway.FaultTimeout)
}

func TestDeadEndpointIsAnUnreachableFault(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, servedOllama)
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

func TestTruncated200CarriesItsObservedUsage(t *testing.T) {
	srv, _ := serving(t, http.StatusOK, `{
		"message": {"role": "assistant", "content": "an ans"},
		"done": false,
		"prompt_eval_count": 10, "eval_count": 5
	}`)

	_, err := complete(t, srv)

	fault := wantFault(t, err, gateway.FaultBadResponse)
	if fault.ObservedUsage == nil || fault.ObservedUsage.TotalTokens != 15 {
		t.Errorf("ObservedUsage = %+v, want the parsed counts — they tighten the double-spend estimate", fault.ObservedUsage)
	}
}
