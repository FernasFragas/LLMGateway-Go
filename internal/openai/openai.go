package openai

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

// OpenAI speaks the /v1/chat/completions dialect
// (developers.openai.com/api/reference/resources/chat/subresources/completions).
// Of the three adapters this one carries the least translation weight: every
// domain role name is already this wire's role name, and tool-call
// arguments are already a JSON-encoded string on both sides.
type OpenAI struct {
	client *http.Client
	apiKey string
}

// NewOpenAI wraps client; nil means http.DefaultClient — deadlines come
// from the per-try context, not the client. apiKey is sent as a Bearer
// token on every request.
func NewOpenAI(client *http.Client, apiKey string) *OpenAI {
	if client == nil {
		client = http.DefaultClient
	}

	return &OpenAI{client: client, apiKey: apiKey}
}

func (o *OpenAI) Complete(ctx context.Context, mp gateway.ModelProvider, req gateway.ChatRequest) (gateway.Completion, error) {
	body, err := json.Marshal(openaiRequest(mp.Model, req))
	if err != nil {
		return gateway.Completion{}, shared.BadResponseFault("openai", fmt.Errorf("encode request: %w", err), nil)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(mp.Endpoint, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return gateway.Completion{}, shared.BadResponseFault("openai", fmt.Errorf("build request: %w", err), nil)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return gateway.Completion{}, shared.TransportFault(err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return gateway.Completion{}, shared.TransportFault(err)
	}

	if resp.StatusCode != http.StatusOK {
		return gateway.Completion{}, shared.StatusFault("openai", resp.StatusCode, raw)
	}

	var wire openaiChatResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return gateway.Completion{}, shared.BadResponseFault("openai", fmt.Errorf("decode response: %w", err), nil)
	}

	return wire.toDomain()
}
