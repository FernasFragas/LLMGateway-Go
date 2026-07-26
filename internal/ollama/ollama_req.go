package ollama

import (
	"encoding/json"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	// Stream is always false: streaming is Ollama's default, and a rejected
	// scope decision here (openapi.yaml) — stated, never assumed.
	Stream  bool           `json:"stream"`
	Tools   []ollamaTool   `json:"tools,omitempty"`
	Options *ollamaOptions `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolName names the tool a tool-role message answers. Ollama matches
	// results to calls by name (it has no tool_call_id), so a multi-tool
	// turn is only unambiguous when this is set.
	ToolName  string           `json:"tool_name,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name string `json:"name"`
	// Arguments is a JSON object on this wire; the domain's verbatim string
	// is exactly that object's bytes, so it passes through unparsed.
	Arguments json.RawMessage `json:"arguments"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ollamaOptions carries the sampling knobs Ollama reads from its options
// object; num_predict is the wire's name for max_tokens.
type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	Stop        []string `json:"stop,omitempty"`
	NumPredict  int      `json:"num_predict"`
}

func ollamaRequest(model string, req gateway.ChatRequest) ollamaChatRequest {
	wire := ollamaChatRequest{
		Model:    model,
		Stream:   false,
		Messages: make([]ollamaMessage, len(req.Messages)),
		Options: &ollamaOptions{
			Temperature: req.Temperature,
			TopP:        req.TopP,
			Stop:        req.Stop,
			NumPredict:  req.MaxTokens,
		},
	}

	// Ollama identifies a tool result by the tool's name, not by a
	// tool_call_id it never issued. The domain's tool message carries only
	// the call id, so map id → name from the assistant tool_calls already
	// seen this conversation and resolve the name when the result arrives.
	// Ordering makes this safe: a call always precedes its result.
	toolNames := map[string]string{}

	for i, m := range req.Messages {
		msg := ollamaMessage{Role: string(m.Role), Content: m.Content}
		if m.Role == gateway.RoleTool {
			msg.ToolName = toolNames[m.ToolCallID]
		}
		for _, tc := range m.ToolCalls {
			toolNames[tc.ID] = tc.Name
			msg.ToolCalls = append(msg.ToolCalls, ollamaToolCall{
				Function: ollamaFunctionCall{Name: tc.Name, Arguments: json.RawMessage(tc.Arguments)},
			})
		}
		wire.Messages[i] = msg
	}

	// Ollama has no tool_choice. "none" is honored by not declaring the
	// tools at all; "auto" and a forced function both declare them — forcing
	// is beyond this dialect, and the model deciding is the closest honest
	// behavior.
	if req.ToolChoice == nil || req.ToolChoice.Mode != gateway.ToolChoiceNone {
		for _, t := range req.Tools {
			wire.Tools = append(wire.Tools, ollamaTool{
				Type:     "function",
				Function: ollamaToolFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
			})
		}
	}

	return wire
}
