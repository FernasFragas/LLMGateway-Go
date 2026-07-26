package openai

import (
	"encoding/json"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

type openaiChatRequest struct {
	Model    string          `json:"model"`
	Messages []openaiMessage `json:"messages"`
	// max_tokens is deprecated in favor of max_completion_tokens on this
	// endpoint; the adapter sends only the current field rather than both,
	// per the decision this repo insists on writing down instead of
	// guessing later.
	MaxCompletionTokens int          `json:"max_completion_tokens"`
	Temperature         *float64     `json:"temperature,omitempty"`
	TopP                *float64     `json:"top_p,omitempty"`
	Stop                []string     `json:"stop,omitempty"`
	Tools               []openaiTool `json:"tools,omitempty"`
	// ToolChoice is either the string "auto" or an openaiToolChoice{type:
	// function}; encoding/json marshals whichever concrete value is set.
	ToolChoice any `json:"tool_choice,omitempty"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openaiFunctionCall `json:"function"`
}

type openaiFunctionCall struct {
	Name string `json:"name"`
	// Arguments is a JSON-encoded string on this wire — the one dialect of
	// the three that already matches the domain's own verbatim-string
	// representation, so no re-encoding happens in either direction.
	Arguments string `json:"arguments"`
}

type openaiTool struct {
	Type     string             `json:"type"` // always "function"
	Function openaiToolFunction `json:"function"`
}

type openaiToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openaiToolChoice struct {
	Type     string                   `json:"type"` // always "function"
	Function openaiToolChoiceFunction `json:"function"`
}

type openaiToolChoiceFunction struct {
	Name string `json:"name"`
}

func openaiRequest(model string, req gateway.ChatRequest) openaiChatRequest {
	wire := openaiChatRequest{
		Model:               model,
		MaxCompletionTokens: req.MaxTokens,
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		Stop:                req.Stop,
		Messages:            make([]openaiMessage, len(req.Messages)),
	}

	for i, m := range req.Messages {
		wire.Messages[i] = openaiRequestMessage(m)
	}

	for _, t := range req.Tools {
		wire.Tools = append(wire.Tools, openaiTool{
			Type:     "function",
			Function: openaiToolFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
		})
	}
	// tool_choice "none" is a real bare-string value on this wire — the tools
	// stay declared and tool_choice tells the model not to call any of them,
	// which also keeps tool_use blocks already in the transcript matched to
	// a declared tool.
	if req.ToolChoice != nil {
		switch req.ToolChoice.Mode {
		case gateway.ToolChoiceNone:
			wire.ToolChoice = "none"
		case gateway.ToolChoiceAuto:
			wire.ToolChoice = "auto"
		case gateway.ToolChoiceFunction:
			wire.ToolChoice = openaiToolChoice{
				Type:     "function",
				Function: openaiToolChoiceFunction{Name: req.ToolChoice.Function},
			}
		}
	}

	return wire
}

// openaiRequestMessage translates one turn. Every domain role name is
// already this wire's role name — the one dialect of the three with no
// role remapping.
func openaiRequestMessage(m gateway.Message) openaiMessage {
	msg := openaiMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}

	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, openaiToolCall{
			ID:       tc.ID,
			Type:     "function",
			Function: openaiFunctionCall{Name: tc.Name, Arguments: tc.Arguments},
		})
	}

	return msg
}
