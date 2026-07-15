package gateway

import (
	"time"
)

// Role identifies the author of a Message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn of a conversation. Content is a plain string — the
// gateway is content-blind (see non-goals): it validates shape at the edge
// and never inspects meaning.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // assistant messages only
	ToolCallID string     // required when Role == RoleTool: the call this message answers
}

// FinishReason says why the model stopped generating.
type FinishReason string

const (
	FinishStop      FinishReason = "stop"
	FinishLength    FinishReason = "length"
	FinishToolCalls FinishReason = "tool_calls"
)

// Usage is the token accounting for one completion.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Completion is what a provider produced for one attempt, already translated
// into domain terms by the provider adapter.
type Completion struct {
	Message      Message
	FinishReason FinishReason
	Usage        Usage
}

// ModelProvider records that one provider endpoint serves one model — a
// transport fact. The model-providers list is the gateway's only knowledge
// of the provider landscape; it never implies two models are
// interchangeable (non-goal: no model intelligence).
type ModelProvider struct {
	Model    string
	Provider string
	Endpoint string
}

// App is an authenticated caller and its declared contract terms.
type App struct {
	Name   string
	Policy FailoverPolicy
	// TotalDeadline overrides the gateway default per-request budget when
	// set (the agent service's long tool-calling runs need a bigger one).
	TotalDeadline time.Duration
}
