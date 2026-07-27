package logs

import (
	"context"
	"errors"
	"testing"
)

func TestFailedRefreshIsLoggedAndTheErrorStillReturned(t *testing.T) {
	log, out := captured(t)
	outage := errors.New("jwks endpoint: connection refused")
	refresh := NewJWKSRefresher(refreshing{err: outage}, log)

	err := refresh.Refresh(context.Background())

	if !errors.Is(err, outage) {
		t.Errorf("error = %v, want the cache's own, unchanged — the decorator observes, never handles", err)
	}
	wantLogged(t, out, "jwks refresh failed", "connection refused")
}

func TestSuccessfulRefreshPassesThroughSilently(t *testing.T) {
	log, out := captured(t)
	refresh := NewJWKSRefresher(refreshing{}, log)

	if err := refresh.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh = %v, want nil passed through", err)
	}
	wantSilence(t, out)
}

// refreshing is a stub JWKS cache failing with the given error.
type refreshing struct {
	err error
}

func (r refreshing) Refresh(context.Context) error { return r.err }
