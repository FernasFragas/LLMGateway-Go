package logs

import (
	"context"
	"errors"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestUnclassifiedCoreErrorIsLoggedAndStillReturned(t *testing.T) {
	log, out := captured(t)
	bug := errors.New("nil pointer somewhere in routing")
	chat := NewChatService(chatting{err: bug}, log)

	_, err := chat.Chat(context.Background(), "sk-test-secret-key", chatRequest())

	if !errors.Is(err, bug) {
		t.Errorf("error = %v, want the core's own, unchanged — the decorator observes, never handles", err)
	}
	wantLogged(t, out, "unclassified core error", "nil pointer somewhere in routing")
	wantNeverLogged(t, out, "sk-test-secret-key", "the caller's private prompt")
}

func TestContractAbidingFailuresPassThroughSilently(t *testing.T) {
	for name, err := range map[string]error{
		"gateway error": &gateway.Error{Code: gateway.CodeQuotaExceeded, Message: "quota exhausted"},
		"disconnect":    context.Canceled,
		"none":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			log, out := captured(t)
			chat := NewChatService(chatting{err: err}, log)

			_, _ = chat.Chat(context.Background(), "sk-test-secret-key", chatRequest())

			wantSilence(t, out)
		})
	}
}

// chatting is a stub core failing with the given error.
type chatting struct {
	err error
}

func (c chatting) Chat(context.Context, string, gateway.ChatRequest) (gateway.ChatResult, error) {
	return gateway.ChatResult{}, c.err
}
