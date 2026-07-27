package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/shared"
)

// maxResponseBytes caps what an adapter will read back — the same ceiling
// every provider adapter in this tree uses.
const maxResponseBytes = 10 << 20

// anthropicVersion pins the Messages API revision this adapter's wire
// structs were written against (docs.anthropic.com/en/api/messages).
// Anthropic requires it on every request; there is no default to fall back
// to.
const anthropicVersion = "2023-06-01"

// Anthropic speaks the /v1/messages dialect. Two of its quirks are absorbed
// here so the core never learns them: the system prompt is a top-level
// field, never a message, and there is no tool role — a tool result travels
// as a user message holding a tool_result block.
type Anthropic struct {
	client *http.Client
	key    shared.Key
}

// NewAnthropic wraps client; nil means http.DefaultClient — deadlines come
// from the per-try context, not the client. key is resolved on every request
// and sent as x-api-key, so a rotation reaches the wire within one request
// instead of waiting for a restart (ADR-001); nil means none.
func NewAnthropic(client *http.Client, key shared.Key) *Anthropic {
	if client == nil {
		client = http.DefaultClient
	}
	if key == nil {
		key = shared.NoKey
	}

	return &Anthropic{client: client, key: key}
}

func (a *Anthropic) Complete(ctx context.Context, mp gateway.ModelProvider, req gateway.ChatRequest) (gateway.Completion, error) {
	body, err := json.Marshal(anthropicRequest(mp.Model, req))
	if err != nil {
		return gateway.Completion{}, shared.BadResponseFault("anthropic", fmt.Errorf("encode request: %w", err), nil)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(mp.Endpoint, "/")+"/messages", bytes.NewReader(body))
	if err != nil {
		return gateway.Completion{}, shared.BadResponseFault("anthropic", fmt.Errorf("build request: %w", err), nil)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.key())
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return gateway.Completion{}, shared.TransportFault(err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return gateway.Completion{}, shared.TransportFault(err)
	}

	if resp.StatusCode != http.StatusOK {
		return gateway.Completion{}, shared.StatusFault("anthropic", resp.StatusCode, raw)
	}

	var wire anthropicChatResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return gateway.Completion{}, shared.BadResponseFault("anthropic", fmt.Errorf("decode response: %w", err), nil)
	}

	return wire.toDomain()
}
