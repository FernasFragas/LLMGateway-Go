package logs

// Test harness: a captured logger and assertions that read the emitted text.

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// captured returns a logger whose every level, debug included, lands in the
// returned buffer.
func captured(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// wantLogged asserts every want appears somewhere in the emitted log.
func wantLogged(t *testing.T, out *bytes.Buffer, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out.String(), want) {
			t.Errorf("log does not mention %q:\n%s", want, out.String())
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
