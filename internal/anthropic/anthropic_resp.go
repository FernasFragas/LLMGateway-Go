package anthropic

import (
	"fmt"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/shared"
)

type anthropicChatResponse struct {
	Content    []anthropicContent `json:"content"`
	StopReason string             `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// anthropicFinish maps every stop_reason this adapter knows how to price.
// Newer reasons (e.g. a future refusal or pause type) fall through to the
// unrecognized branch in toDomain rather than silently defaulting — a
// finish reason the caller can't trust is worse than an honest fault.
var anthropicFinish = map[string]gateway.FinishReason{
	"end_turn":      gateway.FinishStop,
	"stop_sequence": gateway.FinishStop,
	"max_tokens":    gateway.FinishLength,
	"tool_use":      gateway.FinishToolCalls,
}

func (r anthropicChatResponse) toDomain() (gateway.Completion, error) {
	usage := gateway.Usage{
		PromptTokens:     r.Usage.InputTokens,
		CompletionTokens: r.Usage.OutputTokens,
		TotalTokens:      r.Usage.InputTokens + r.Usage.OutputTokens,
	}

	msg := gateway.Message{Role: gateway.RoleAssistant}
	for _, block := range r.Content {
		switch block.Type {
		case "text":
			msg.Content += block.Text
		case "tool_use":
			// Anthropic mints its own tool_use id — unlike Ollama, nothing
			// here needs minting.
			msg.ToolCalls = append(msg.ToolCalls, gateway.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: string(block.Input),
			})
		}
	}

	finish, ok := anthropicFinish[r.StopReason]
	if !ok {
		// Whatever usage was still reported tightens the double-spend
		// estimate even though the shape wasn't one this adapter trusts.
		observed := usage
		return gateway.Completion{}, shared.BadResponseFault("anthropic",
			fmt.Errorf("unrecognized stop_reason %q", r.StopReason), &observed)
	}

	return gateway.Completion{Message: msg, FinishReason: finish, Usage: usage}, nil
}
