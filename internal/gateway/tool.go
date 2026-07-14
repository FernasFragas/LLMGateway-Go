package gateway

import "encoding/json"

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

// Tool declares a function the model may call.
type Tool struct {
	Name        string
	Description string
	// Parameters is the JSON Schema for the function arguments — carried
	// through to the provider, never interpreted.
	Parameters json.RawMessage
}
