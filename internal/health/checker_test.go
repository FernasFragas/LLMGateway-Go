package health

// The probe contract: liveness is unconditional, readiness fails closed on
// the first failing condition and names it.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLiveIsUnconditional(t *testing.T) {
	c := NewChecker()
	c.AddReadiness("key-cache", func(context.Context) error { return errors.New("no keys loaded") })

	if err := c.Live(); err != nil {
		t.Errorf("Live = %v — a dependency being down must never restart the process", err)
	}
}

func TestReadyWithNoConditionsPasses(t *testing.T) {
	if err := NewChecker().Ready(context.Background()); err != nil {
		t.Errorf("Ready = %v, want nil", err)
	}
}

func TestReadyFailsClosedAndNamesTheCondition(t *testing.T) {
	c := NewChecker()
	c.AddReadiness("key-cache", func(context.Context) error { return errors.New("no keys loaded") })

	err := c.Ready(context.Background())
	if err == nil {
		t.Fatal("Ready = nil, want the failing condition")
	}
	if !strings.Contains(err.Error(), "key-cache") {
		t.Errorf("Ready = %q, want the condition named for the probe log", err)
	}
}

func TestReadyRecoversWhenTheConditionHolds(t *testing.T) {
	c := NewChecker()
	warm := false
	c.AddReadiness("key-cache", func(context.Context) error {
		if !warm {
			return errors.New("no keys loaded")
		}
		return nil
	})

	if err := c.Ready(context.Background()); err == nil {
		t.Fatal("cold: Ready = nil, want refusal")
	}
	warm = true
	if err := c.Ready(context.Background()); err != nil {
		t.Errorf("warm: Ready = %v, want nil", err)
	}
}
