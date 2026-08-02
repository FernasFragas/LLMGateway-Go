package logs

import (
	"context"
	"errors"
	"testing"
)

func TestFailedRefreshIsLoggedWithItsProviderAndTheErrorStillReturned(t *testing.T) {
	log, out := captured(t)
	outage := errors.New("vault read secret/data/llm/openai: status 503")
	refresh := NewRefresher(refreshing{err: outage}, log)

	err := refresh.Refresh(context.Background(), "openai")

	if !errors.Is(err, outage) {
		t.Errorf("error = %v, want the cache's own, unchanged — the decorator observes, never handles", err)
	}
	// "provider key", never just "key": a reader must not have to guess whether
	// a failed-refresh line came from this cache or from internal/auth's JWKS
	// one, and the provider name is what scopes the blast radius to one route.
	wantLogged(t, out, "provider key refresh failed", "openai", "status 503")
}

func TestSuccessfulRefreshPassesThroughSilently(t *testing.T) {
	log, out := captured(t)
	refresh := NewRefresher(refreshing{}, log)

	if err := refresh.Refresh(context.Background(), "anthropic"); err != nil {
		t.Fatalf("Refresh = %v, want nil passed through", err)
	}
	wantSilence(t, out)
}

// refreshing is a stub key cache failing with the given error.
type refreshing struct {
	err error
}

func (r refreshing) Refresh(context.Context, string) error { return r.err }
