package ollama

import (
	"fmt"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/shared"
)

type ollamaChatResponse struct {
	Message struct {
		Role      string           `json:"role"`
		Content   string           `json:"content"`
		ToolCalls []ollamaToolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

func (r ollamaChatResponse) toDomain() (gateway.Completion, error) {
	usage := gateway.Usage{
		PromptTokens:     r.PromptEvalCount,
		CompletionTokens: r.EvalCount,
		TotalTokens:      r.PromptEvalCount + r.EvalCount,
	}

	// A non-streaming 200 that is not done is a truncated exchange the
	// provider likely billed — whatever counts it carried tighten the
	// double-spend estimate.
	if !r.Done {
		observed := usage
		return gateway.Completion{}, shared.BadResponseFault("ollama", fmt.Errorf("response not done (done_reason %q)", r.DoneReason), &observed)
	}

	msg := gateway.Message{Role: gateway.RoleAssistant, Content: r.Message.Content}
	for i, tc := range r.Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, gateway.ToolCall{
			// Ollama names calls but never IDs them; the contract requires
			// an id, so the adapter mints one — unique within the message,
			// which is all a tool_call_id must be.
			ID:        fmt.Sprintf("call_%d", i+1),
			Name:      tc.Function.Name,
			Arguments: string(tc.Function.Arguments),
		})
	}

	finish := gateway.FinishStop
	switch {
	case len(msg.ToolCalls) > 0:
		finish = gateway.FinishToolCalls
	case r.DoneReason == "length":
		finish = gateway.FinishLength
	}

	return gateway.Completion{Message: msg, FinishReason: finish, Usage: usage}, nil
}
