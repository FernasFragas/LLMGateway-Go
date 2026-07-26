package openai

import (
	"errors"
	"fmt"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/shared"
)

type openaiChatResponse struct {
	Choices []struct {
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// openaiFinish maps every finish_reason this adapter knows how to price.
// "content_filter" is deliberately absent: the domain has no concept of a
// safety-filtered answer, and presenting it as an ordinary FinishStop would
// tell the caller it received a complete answer it did not. It falls
// through to the unrecognized branch in toDomain like any other reason this
// adapter doesn't trust.
var openaiFinish = map[string]gateway.FinishReason{
	"stop":       gateway.FinishStop,
	"length":     gateway.FinishLength,
	"tool_calls": gateway.FinishToolCalls,
}

func (r openaiChatResponse) toDomain() (gateway.Completion, error) {
	usage := gateway.Usage{
		PromptTokens:     r.Usage.PromptTokens,
		CompletionTokens: r.Usage.CompletionTokens,
		TotalTokens:      r.Usage.TotalTokens,
	}

	if len(r.Choices) == 0 {
		return gateway.Completion{}, shared.BadResponseFault("openai", errors.New("response holds no choices"), &usage)
	}
	choice := r.Choices[0]

	finish, ok := openaiFinish[choice.FinishReason]
	if !ok {
		observed := usage
		return gateway.Completion{}, shared.BadResponseFault("openai",
			fmt.Errorf("unrecognized finish_reason %q", choice.FinishReason), &observed)
	}

	msg := gateway.Message{Role: gateway.RoleAssistant, Content: choice.Message.Content}
	for _, tc := range choice.Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, gateway.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	return gateway.Completion{Message: msg, FinishReason: finish, Usage: usage}, nil
}
