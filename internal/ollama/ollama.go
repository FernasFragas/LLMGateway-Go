package ollama

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

// maxResponseBytes caps what an adapter will read back — num_predict bounds
// the tokens, this bounds the bytes a misbehaving endpoint could feed us.
const maxResponseBytes = 10 << 20

// Ollama speaks the /api/chat dialect (docs.ollama.com/api/chat). Two of
// its quirks are absorbed here so the core never learns them: streaming is
// its default and must be refused explicitly, and tool-call arguments are a
// JSON object on this wire while the domain carries them verbatim as a
// string.
type Ollama struct {
	client *http.Client
	apiKey string
}

// NewOllama wraps client; nil means http.DefaultClient — deadlines come
// from the per-try context, not the client.
//
// apiKey selects the auth mode by its presence, not a flag: a self-hosted,
// in-cluster instance (http://ollama…:11434) needs no credential, so an
// empty key sends no Authorization header — today's behavior, now explicit;
// Ollama Cloud (https://ollama.com) is Bearer-authenticated, so a non-empty
// key rides on every request. Whether a given route is self-hosted or cloud
// is decided by its Endpoint at wiring time, and the matching key (or none)
// is resolved and handed in here — see ADR-001.
func NewOllama(client *http.Client, apiKey string) *Ollama {
	if client == nil {
		client = http.DefaultClient
	}

	return &Ollama{client: client, apiKey: apiKey}
}

func (o *Ollama) Complete(ctx context.Context, mp gateway.ModelProvider, req gateway.ChatRequest) (gateway.Completion, error) {
	body, err := json.Marshal(ollamaRequest(mp.Model, req))
	if err != nil {
		return gateway.Completion{}, shared.BadResponseFault("ollama", fmt.Errorf("encode request: %w", err), nil)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(mp.Endpoint, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return gateway.Completion{}, shared.BadResponseFault("ollama", fmt.Errorf("build request: %w", err), nil)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Bearer only when a key is configured (Ollama Cloud); a self-hosted
	// endpoint gets no Authorization header at all — see ADR-001.
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

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
		return gateway.Completion{}, shared.StatusFault("ollama", resp.StatusCode, raw)
	}

	var wire ollamaChatResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return gateway.Completion{}, shared.BadResponseFault("ollama", fmt.Errorf("decode response: %w", err), nil)
	}

	return wire.toDomain()
}
