package gateway

import (
	"encoding/json"
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

// Tool declares a function the model may call.
type Tool struct {
	Name        string
	Description string
	// Parameters is the JSON Schema for the function arguments — carried
	// through to the provider, never interpreted.
	Parameters json.RawMessage
}

// ToolCall is a function invocation produced by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON-encoded, exactly as the model produced it
}

// ToolChoiceMode says how the model may use the declared tools.
type ToolChoiceMode string

const (
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceFunction ToolChoiceMode = "function"
)

// ToolChoice constrains tool use for one request. Function is set only when
// Mode == ToolChoiceFunction.
type ToolChoice struct {
	Mode     ToolChoiceMode
	Function string
}

// ChatRequest is one chat completion in the gateway's own vocabulary — the
// supported subset of the wire schema, already parsed. Unsupported wire
// parameters (stream, n > 1, multimodal content) are rejected at the edge
// and never reach this type.
type ChatRequest struct {
	Model       string
	Messages    []Message
	MaxTokens   int // required: bounds cost per request — and the unobserved-spend estimate
	Temperature *float64
	TopP        *float64
	Stop        []string // at most 4
	Tools       []Tool
	ToolChoice  *ToolChoice
}

// Validate checks the domain invariants the core relies on. The edge
// validates the full wire schema; this is the core refusing to operate on
// requests it cannot price or route.
func (r ChatRequest) Validate() error {
	switch {
	case r.Model == "":
		return &Error{Code: CodeInvalidRequest, Message: "model is required"}
	case len(r.Messages) == 0:
		return &Error{Code: CodeInvalidRequest, Message: "messages must contain at least one message"}
	case r.MaxTokens < 1:
		return &Error{Code: CodeInvalidRequest, Message: "max_tokens is required and must be at least 1"}
	case len(r.Stop) > 4:
		return &Error{Code: CodeInvalidRequest, Message: "stop accepts at most 4 sequences"}
	}
	return nil
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

// ChatResult is the outcome the caller receives. Model names what actually
// served the request — never an echo of what was asked for (decision #5).
type ChatResult struct {
	Model        string
	Provider     string
	Substituted  bool // true iff a model other than the requested one served
	Message      Message
	FinishReason FinishReason
	Usage        Usage
}

// Route records that one provider endpoint serves one model — a transport
// fact. The routes list is the gateway's only knowledge of the provider
// landscape; it never implies two models are interchangeable (non-goal:
// no model intelligence).
type Route struct {
	Model    string
	Provider string
	Endpoint string
}

// PolicyKind is the shape of an app's substitution contract.
type PolicyKind string

const (
	// PolicySameModel permits route failover only: the identical model via
	// another route. Same weights, second host — a transport fact, always
	// allowed. No substitutes, ever.
	PolicySameModel PolicyKind = "same-model"
	// PolicyAllowlist permits the substitutes the app itself named, tried in
	// the app's preference order.
	PolicyAllowlist PolicyKind = "allowlist"
	// PolicyAny takes whatever the gateway's failover order offers —
	// availability over fidelity.
	PolicyAny PolicyKind = "any"
)

// FailoverPolicy is an app's substitution contract: the semantic judgment of
// which models may stand in for the one it asked for. It belongs to the app
// alone — the gateway never makes that judgment (decision #5). The zero
// value behaves as same-model: substitution is opt-in.
type FailoverPolicy struct {
	Kind      PolicyKind
	Allowlist []string // Kind == PolicyAllowlist only: acceptable substitutes, preference order
}

// App is an authenticated caller and its declared contract terms.
type App struct {
	Name   string
	Policy FailoverPolicy
	// TotalDeadline overrides the gateway default per-request budget when
	// set (the agent service's long tool-calling runs need a bigger one).
	TotalDeadline time.Duration
}
