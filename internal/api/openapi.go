package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// chatRequestWire is the ChatRequest schema from openapi.yaml, field for
// field. Pointers distinguish "absent" from zero where the difference
// matters (stream: false is fine; stream: true is refused).
type chatRequestWire struct {
	Model       string          `json:"model"`
	Messages    []messageWire   `json:"messages"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature"`
	TopP        *float64        `json:"top_p"`
	Stop        stopSequences   `json:"stop"`
	Tools       []toolWire      `json:"tools"`
	ToolChoice  *toolChoiceWire `json:"tool_choice"`
	N           *int            `json:"n"`
	Stream      *bool           `json:"stream"`
}

type messageWire struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content,omitempty"` // nullable: assistant messages carrying tool_calls
	ToolCalls  []toolCallWire `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type toolCallWire struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function toolFunctionCall `json:"function"`
}

type toolFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolWire struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// reject enforces the wire schema the decoder can't: enum fields, the
// sampling ranges the schema promises, and the scope decisions (stream and
// n > 1 are refused, not ignored). Domain
// invariants — model present, max_tokens ≥ 1 — stay with the core's
// Validate; checking them twice would fork the contract.
func (wr chatRequestWire) reject() *apiError {
	if wr.Stream != nil && *wr.Stream {
		return unsupportedParameter("stream", "'stream' is not supported.")
	}
	if wr.N != nil && *wr.N != 1 {
		return unsupportedParameter("n", "only n = 1 is supported.")
	}
	if wr.Temperature != nil && (*wr.Temperature < 0 || *wr.Temperature > 2) {
		return invalidRequest("temperature must be between 0 and 2")
	}
	if wr.TopP != nil && (*wr.TopP < 0 || *wr.TopP > 1) {
		return invalidRequest("top_p must be between 0 and 1")
	}

	for i, m := range wr.Messages {
		switch m.Role {
		case "system", "user", "assistant", "tool":
		default:
			return invalidRequest(fmt.Sprintf("messages[%d].role must be one of system, user, assistant, tool", i))
		}
		if m.Content == nil && !(m.Role == "assistant" && len(m.ToolCalls) > 0) {
			return invalidRequest(fmt.Sprintf("messages[%d].content is required", i))
		}
		if m.Role == "tool" && m.ToolCallID == "" {
			return invalidRequest(fmt.Sprintf("messages[%d].tool_call_id is required when role is tool", i))
		}
		if m.Role != "assistant" && len(m.ToolCalls) > 0 {
			return invalidRequest(fmt.Sprintf("messages[%d].tool_calls is allowed on assistant messages only", i))
		}
	}

	for i, t := range wr.Tools {
		if t.Type != "function" {
			return invalidRequest(fmt.Sprintf(`tools[%d].type must be "function"`, i))
		}
		if t.Function.Name == "" {
			return invalidRequest(fmt.Sprintf("tools[%d].function.name is required", i))
		}
	}

	return nil
}

func (wr chatRequestWire) toDomain() gateway.ChatRequest {
	req := gateway.ChatRequest{
		Model:       wr.Model,
		MaxTokens:   wr.MaxTokens,
		Temperature: wr.Temperature,
		TopP:        wr.TopP,
		Stop:        wr.Stop,
	}

	req.Messages = make([]gateway.Message, len(wr.Messages))
	for i, m := range wr.Messages {
		msg := gateway.Message{Role: gateway.Role(m.Role), ToolCallID: m.ToolCallID}
		if m.Content != nil {
			msg.Content = *m.Content
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, gateway.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		req.Messages[i] = msg
	}

	for _, t := range wr.Tools {
		req.Tools = append(req.Tools, gateway.Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}

	if wr.ToolChoice != nil {
		req.ToolChoice = &gateway.ToolChoice{
			Mode:     gateway.ToolChoiceMode(wr.ToolChoice.Mode),
			Function: wr.ToolChoice.Function,
		}
	}

	return req
}

// stopSequences accepts the schema's string-or-array shape.
type stopSequences []string

func (s *stopSequences) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*s = stopSequences{one}

		return nil
	}

	return json.Unmarshal(b, (*[]string)(s))
}

// toolChoiceWire accepts "none" | "auto" | {type: function, function: {name}}.
type toolChoiceWire struct {
	Mode     string
	Function string
}

func (tc *toolChoiceWire) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var mode string
		if err := json.Unmarshal(b, &mode); err != nil {
			return err
		}

		if mode != "none" && mode != "auto" {
			return &wireError{param: "tool_choice", message: `tool_choice must be "none", "auto", or a function object`}
		}
		tc.Mode = mode

		return nil
	}

	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	if obj.Type != "function" || obj.Function.Name == "" {
		return &wireError{param: "tool_choice", message: `tool_choice object must be {"type": "function", "function": {"name": ...}}`}
	}

	tc.Mode = "function"
	tc.Function = obj.Function.Name

	return nil
}

// wireError is a schema violation found inside a custom unmarshaler, kept
// typed so decodeError can name the parameter instead of echoing a raw
// json error.
type wireError struct {
	param   string
	message string
}

func (e *wireError) Error() string { return e.message }

func decodeChatRequest(r *http.Request) (chatRequestWire, *apiError) {
	if apiErr := requireJSON(r); apiErr != nil {
		return chatRequestWire{}, apiErr
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // unknown params rejected, never dropped — a hidden semantic change

	var wire chatRequestWire
	if err := dec.Decode(&wire); err != nil {
		return wire, decodeError(err)
	}

	if apiErr := wire.reject(); apiErr != nil {
		return wire, apiErr
	}

	return wire, nil
}

// requireJSON enforces the request media type the spec declares: the body is
// application/json, stated — never guessed from the bytes. Parameters like
// charset are tolerated; a different or absent type is refused.
func requireJSON(r *http.Request) *apiError {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return &apiError{
			status:  http.StatusUnsupportedMediaType,
			code:    codeUnsupportedMediaType,
			message: "Content-Type must be application/json",
		}
	}

	return nil
}

// decodeError names what was wrong with the bytes, in the contract's codes.
func decodeError(err error) *apiError {
	var maxBytes *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	var wireErr *wireError

	switch {
	case errors.As(err, &maxBytes):
		return &apiError{
			status:  http.StatusRequestEntityTooLarge,
			code:    codePayloadTooLarge,
			message: fmt.Sprintf("request body exceeds the %d-byte maximum", maxBytes.Limit),
		}
	case errors.As(err, &wireErr):
		return &apiError{
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: wireErr.message,
			details: &errorDetails{Param: wireErr.param},
		}
	case errors.As(err, &typeErr):
		// Covers multimodal content blocks too: content as an array is a
		// type mismatch on a string field — rejected by scope decision.
		return invalidRequest(fmt.Sprintf("%s must be a %s", typeErr.Field, typeErr.Type))
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		param := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return unsupportedParameter(param, fmt.Sprintf("'%s' is not a supported parameter.", param))
	case errors.Is(err, io.EOF):
		return invalidRequest("request body is required")
	default:
		return invalidRequest("request body is not valid JSON")
	}
}

type chatResponseWire struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []choiceWire `json:"choices"`
	Usage   usageWire    `json:"usage"`
}

type choiceWire struct {
	Index        int         `json:"index"`
	Message      messageWire `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type usageWire struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func chatResponse(res gateway.ChatResult) chatResponseWire {
	return chatResponseWire{
		ID:      "chatcmpl-" + randomID(8),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   res.Model, // what actually served — never an echo (decision #5)
		Choices: []choiceWire{{
			Index:        0,
			Message:      messageToWire(res.Message),
			FinishReason: string(res.FinishReason),
		}},
		Usage: usageWire{
			PromptTokens:     res.Usage.PromptTokens,
			CompletionTokens: res.Usage.CompletionTokens,
			TotalTokens:      res.Usage.TotalTokens,
		},
	}
}

func messageToWire(m gateway.Message) messageWire {
	wire := messageWire{Role: string(m.Role)}

	for _, tc := range m.ToolCalls {
		wire.ToolCalls = append(wire.ToolCalls, toolCallWire{
			ID:       tc.ID,
			Type:     "function",
			Function: toolFunctionCall{Name: tc.Name, Arguments: tc.Arguments},
		})
	}

	// Content is null only on assistant messages that carry tool calls and
	// nothing else — mirroring what the schema accepts inbound.
	if m.Content != "" || len(m.ToolCalls) == 0 {
		content := m.Content
		wire.Content = &content
	}

	return wire
}
