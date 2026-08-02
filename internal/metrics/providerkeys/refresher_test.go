package metrics

import (
	"context"
	"errors"
	"testing"
)

func TestFailedRefreshIsCountedAndTheErrorStillReturned(t *testing.T) {
	outage := errors.New("secret store unreachable")
	refresh := NewRefresher(refreshing{err: outage})

	err := refresh.Refresh(context.Background(), "openai")

	if !errors.Is(err, outage) {
		t.Errorf("error = %v, want the cache's own, unchanged — the decorator counts, never handles", err)
	}
	if got := refresh.Refreshes(); got != 1 {
		t.Errorf("Refreshes = %d, want the failed attempt counted too — a flat line here with failures rising means the loop died", got)
	}
	if got := refresh.Failures(); got != 1 {
		t.Errorf("Failures = %d, want 1 — fail static is only defensible while the failing is visible", got)
	}
}

func TestSuccessfulRefreshCountsAsAttemptOnly(t *testing.T) {
	refresh := NewRefresher(refreshing{})

	if err := refresh.Refresh(context.Background(), "openai"); err != nil {
		t.Fatalf("Refresh = %v, want nil passed through", err)
	}
	if got := refresh.Refreshes(); got != 1 {
		t.Errorf("Refreshes = %d, want 1", got)
	}
	if got := refresh.Failures(); got != 0 {
		t.Errorf("Failures = %d, want 0 — a healthy rotation must read as healthy", got)
	}
}

// refreshing is a stub key cache failing with the given error.
type refreshing struct {
	err error
}

func (r refreshing) Refresh(context.Context, string) error { return r.err }
