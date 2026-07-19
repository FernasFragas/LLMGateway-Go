package auth

// The cache's fail-static clauses: cold refuses readiness, warm serves from
// memory, and no refresh failure — outage or garbage — ever empties it.

import (
	"context"
	"testing"
)

func TestColdCacheRefusesReadiness(t *testing.T) {
	s := newSigner(t)

	cache := coldCache(t, jwksServer(t, s.jwks()).URL)

	if err := cache.Ready(context.Background()); err == nil {
		t.Error("a cache that never loaded keys must refuse readiness — fail closed where no traffic is harmed")
	}
}

func TestWarmCacheIsReady(t *testing.T) {
	if err := warmCache(t, newSigner(t)).Ready(context.Background()); err != nil {
		t.Errorf("Ready = %v, want nil once keys are loaded", err)
	}
}

func TestFailedRefreshKeepsServingOldKeys(t *testing.T) {
	s := newSigner(t)
	srv := jwksServer(t, s.jwks())
	cache := coldCache(t, srv.URL)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	srv.Close() // the issuer goes dark

	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh against a dead endpoint must report the failure")
	}
	if _, ok := directory(t, cache).AppForKey(context.Background(), s.mint(t, nil)); !ok {
		t.Error("stale keys must keep serving — stale-but-usable beats unavailable")
	}
	if err := cache.Ready(context.Background()); err != nil {
		t.Errorf("Ready = %v, want nil — the pod still holds usable keys", err)
	}
}

func TestGarbageDocumentDoesNotEmptyTheCache(t *testing.T) {
	s := newSigner(t)
	srv := jwksServer(t, s.jwks())
	cache := coldCache(t, srv.URL)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	empty := coldCache(t, jwksServer(t, []byte(`{"keys":[]}`)).URL)
	if err := empty.Refresh(context.Background()); err == nil {
		t.Error("an empty key list must be a refresh failure, not a working empty cache")
	}

	cache2 := coldCache(t, jwksServer(t, []byte(`not json`)).URL)
	if err := cache2.Refresh(context.Background()); err == nil {
		t.Error("an unparseable document must be a refresh failure")
	}

	if _, ok := directory(t, cache).AppForKey(context.Background(), s.mint(t, nil)); !ok {
		t.Error("the warm cache must be untouched by another cache's failures")
	}
}
