package logs

import (
	"context"
	"errors"
	"log/slog"

	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// ProviderClient logs each attempt's outcome by model provider, never by
// content.
// A fault's Cause has exactly one outlet — this log line; the error itself
// passes through unchanged for the core to classify.
type ProviderClient struct {
	provider gateway.ProviderClient
	log      *slog.Logger
}

// NewProviderClient wraps next; a nil log means slog.Default().
func NewProviderClient(next gateway.ProviderClient, log *slog.Logger) *ProviderClient {
	return &ProviderClient{provider: next, log: logs.OrDefault(log)}
}

func (c *ProviderClient) Complete(ctx context.Context, mp gateway.ModelProvider, req gateway.ChatRequest) (gateway.Completion, error) {
	start := time.Now()
	completion, err := c.provider.Complete(ctx, mp, req)
	if err != nil {
		attrs := []any{
			"provider", mp.Provider,
			"model", mp.Model,
			"elapsed", time.Since(start),
		}
		var fault *gateway.ProviderFault
		if errors.As(err, &fault) {
			attrs = append(attrs, "kind", string(fault.Kind), "cause", fault.Cause)
		} else {
			attrs = append(attrs, "cause", err)
		}
		c.log.WarnContext(ctx, "provider attempt failed", attrs...)
		return completion, err
	}

	c.log.DebugContext(ctx, "provider attempt served",
		"provider", mp.Provider,
		"model", mp.Model,
		"elapsed", time.Since(start),
		"total_tokens", completion.Usage.TotalTokens,
	)
	return completion, nil
}
