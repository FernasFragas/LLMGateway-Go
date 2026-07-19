package logs

import (
	"context"
	"errors"
	"log/slog"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"

	"github.com/FernasFragas/LLMGateway-Go/internal/api"
	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// ChatService logs the one failure the edge answers with a detail-free 500:
// an error outside the core's contract (*gateway.Error or a caller
// disconnect). That is a bug, and this line is its only outlet — the error
// itself passes through unchanged for the edge to answer. Contract-abiding
// outcomes pass in silence; their facts are logged where they happen.
type ChatService struct {
	chatService api.ChatService
	log         *slog.Logger
}

// NewChatService wraps next; a nil log means slog.Default().
func NewChatService(next api.ChatService, log *slog.Logger) *ChatService {
	return &ChatService{chatService: next, log: logs.OrDefault(log)}
}

func (c *ChatService) Chat(ctx context.Context, apiKey string, req gateway.ChatRequest) (gateway.ChatResult, error) {
	res, err := c.chatService.Chat(ctx, apiKey, req)

	var gwErr *gateway.Error
	if err != nil && !errors.As(err, &gwErr) && !errors.Is(err, context.Canceled) {
		c.log.ErrorContext(ctx, "chat: unclassified core error", "err", err)
	}

	return res, err
}
