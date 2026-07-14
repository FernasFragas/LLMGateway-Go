package gateway

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
