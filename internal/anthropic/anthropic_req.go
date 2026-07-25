package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

type anthropicChatRequest struct {
	Model         string               `json:"model"`
	MaxTokens     int                  `json:"max_tokens"`
	System        string               `json:"system,omitempty"`
	Messages      []anthropicMessage   `json:"messages"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	StopSequences []string             `json:"stop_sequences,omitempty"`
	Tools         []anthropicTool      `json:"tools,omitempty"`
	ToolChoice    *anthropicToolChoice `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

// anthropicContent is one content block. A single struct serves all three
// shapes this adapter produces or consumes — text, tool_use, tool_result —
// each tagging only the fields it uses; omitempty keeps the rest off the
// wire. The comment on each field names the block type that owns it.
type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"` // text

	ID    string          `json:"id,omitempty"`    // tool_use
	Name  string          `json:"name,omitempty"`  // tool_use
	Input json.RawMessage `json:"input,omitempty"` // tool_use

	ToolUseID string `json:"tool_use_id,omitempty"` // tool_result
	Content   string `json:"content,omitempty"`     // tool_result
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"` // set only when Type == "tool"
}

func anthropicRequest(model string, req gateway.ChatRequest) anthropicChatRequest {
	wire := anthropicChatRequest{
		Model:         model,
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
	}

	// The system prompt is a top-level field on this wire, never a message —
	// Anthropic rejects a "system" role inside the messages array. Every
	// other turn keeps its place in order.
	var system []string
	for _, m := range req.Messages {
		if m.Role == gateway.RoleSystem {
			system = append(system, m.Content)
			continue
		}
		wire.Messages = append(wire.Messages, anthropicRequestMessage(m))
	}
	wire.System = strings.Join(system, "\n\n")

	for _, t := range req.Tools {
		wire.Tools = append(wire.Tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}
	// Unlike Ollama, this wire has a real tool_choice "none" — the model still
	// sees the declared tools but is told not to call any, which also keeps
	// tool_use blocks already in the transcript matched to a declared tool.
	if req.ToolChoice != nil {
		switch req.ToolChoice.Mode {
		case gateway.ToolChoiceNone:
			wire.ToolChoice = &anthropicToolChoice{Type: "none"}
		case gateway.ToolChoiceAuto:
			wire.ToolChoice = &anthropicToolChoice{Type: "auto"}
		case gateway.ToolChoiceFunction:
			wire.ToolChoice = &anthropicToolChoice{Type: "tool", Name: req.ToolChoice.Function}
		}
	}

	return wire
}

// anthropicRequestMessage translates one non-system turn. Anthropic has no
// tool role: a tool result travels as a user message holding a tool_result
// block that names, by id, the tool_use block it answers.
func anthropicRequestMessage(m gateway.Message) anthropicMessage {
	if m.Role == gateway.RoleTool {
		return anthropicMessage{
			Role: "user",
			Content: []anthropicContent{{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}},
		}
	}

	msg := anthropicMessage{Role: string(m.Role)}
	if m.Content != "" {
		msg.Content = append(msg.Content, anthropicContent{Type: "text", Text: m.Content})
	}
	// Anthropic has no bare tool_call_id: a tool_use block's own id is what a
	// later tool_result names, so the domain's call id carries straight
	// across with no re-minting.
	for _, tc := range m.ToolCalls {
		msg.Content = append(msg.Content, anthropicContent{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Name,
			Input: json.RawMessage(tc.Arguments),
		})
	}

	return msg
}
