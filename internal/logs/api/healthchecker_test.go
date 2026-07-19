package logs

import (
	"context"
	"errors"
	"testing"
)

func TestReadinessRefusalReasonIsLoggedAndTheErrorStillReturned(t *testing.T) {
	log, out := captured(t)
	refusal := errors.New("key-cache: no API keys loaded")
	checker := NewHealthChecker(probes{ready: refusal}, log)

	err := checker.Ready(context.Background())

	if !errors.Is(err, refusal) {
		t.Errorf("error = %v, want the checker's own, unchanged — the decorator observes, never handles", err)
	}
	wantLogged(t, out, "readiness refused", "key-cache: no API keys loaded")
}

func TestReadyPodPassesThroughSilently(t *testing.T) {
	log, out := captured(t)
	checker := NewHealthChecker(probes{}, log)

	if err := checker.Ready(context.Background()); err != nil {
		t.Fatalf("Ready = %v, want nil passed through", err)
	}
	if err := checker.Live(); err != nil {
		t.Fatalf("Live = %v, want nil passed through", err)
	}
	wantSilence(t, out)
}

// probes is a stub health checker refusing readiness with the given error.
type probes struct {
	ready error
}

func (p probes) Live() error                 { return nil }
func (p probes) Ready(context.Context) error { return p.ready }
