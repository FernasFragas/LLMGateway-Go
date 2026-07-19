package logs

// Test harness: a captured logger and assertions that read the emitted text.
// Builders hide mechanics, never meaning — each test states one clause of
// the logging contract: what must reach the log, and — just as binding —
// what never may (API keys, message content).

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// chatRequest carries recognizable private content so content-blindness
// tests can assert it never surfaces.
func chatRequest() gateway.ChatRequest {
	return gateway.ChatRequest{
		Model:     "gpt-4.1",
		Messages:  []gateway.Message{{Role: gateway.RoleUser, Content: "the caller's private prompt"}},
		MaxTokens: 512,
	}
}

// captured returns a logger whose every level, debug included, lands in the
// returned buffer.
func captured(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// --- assertions ---

// wantLogged asserts every want appears somewhere in the emitted log.
func wantLogged(t *testing.T, out *bytes.Buffer, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out.String(), want) {
			t.Errorf("log does not mention %q:\n%s", want, out.String())
		}
	}
}

// wantNeverLogged asserts no secret appears anywhere in the emitted log.
func wantNeverLogged(t *testing.T, out *bytes.Buffer, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(out.String(), secret) {
			t.Errorf("%q reached the log — it must never:\n%s", secret, out.String())
		}
	}
}

// wantSilence asserts the decorator emitted nothing at all.
func wantSilence(t *testing.T, out *bytes.Buffer) {
	t.Helper()
	if out.Len() != 0 {
		t.Errorf("expected silence, logged:\n%s", out.String())
	}
}
