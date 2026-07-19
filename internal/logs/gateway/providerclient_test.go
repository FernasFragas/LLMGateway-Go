package logs

import (
	"context"
	"errors"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestProviderFaultCauseHasItsOnlyOutletInTheLog(t *testing.T) {
	log, out := captured(t)
	fault := &gateway.ProviderFault{Kind: gateway.FaultServerError, Cause: errors.New("openai said: 500 internal error")}
	pc := NewProviderClient(provider{err: fault}, log)

	_, err := pc.Complete(context.Background(), gptOpenAI, chatRequest())

	var got *gateway.ProviderFault
	if !errors.As(err, &got) || got != fault {
		t.Errorf("error = %v, want the fault unchanged — classification is the core's job", err)
	}
	wantLogged(t, out, "openai", "server_error", "500 internal error")
}

func TestMessageContentNeverReachesTheLog(t *testing.T) {
	log, out := captured(t) // debug enabled: even the chattiest level stays content-blind
	pc := NewProviderClient(provider{completion: gateway.Completion{
		Message:      gateway.Message{Role: gateway.RoleAssistant, Content: "the model's private answer"},
		FinishReason: gateway.FinishStop,
		Usage:        gateway.Usage{TotalTokens: 51},
	}}, log)

	_, err := pc.Complete(context.Background(), gptOpenAI, chatRequest())

	if err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Error("a served attempt left no trace at debug level")
	}
	wantNeverLogged(t, out, "the caller's private prompt", "the model's private answer")
}
