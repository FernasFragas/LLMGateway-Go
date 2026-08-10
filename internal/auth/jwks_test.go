package auth

// The cache's fail-static clauses: cold refuses readiness, warm serves from
// memory, and no refresh failure — outage or garbage — ever empties it.

import (
	"context"
	"testing"
	"time"
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

func TestOversizedDocumentDoesNotEmptyTheCache(t *testing.T) {
	s := newSigner(t)
	doc := s.jwks()
	cache := coldCache(t, jwksServerServing(t, doc, oversized(t, doc)).URL)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	if err := cache.Refresh(context.Background()); err == nil {
		t.Error("a document past the size cap must be a refresh failure, not an unbounded read")
	}
	if _, ok := directory(t, cache).AppForKey(context.Background(), s.mint(t, nil)); !ok {
		t.Error("stale keys must keep serving — an oversized answer is one more refresh failure")
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

func TestAColdCacheReportsNoAgeRatherThanZero(t *testing.T) {
	// Not a zero: measuring from the zero time answers ~292 years, which
	// would trip every staleness alert on a pod that is merely starting.
	// Reporting nothing is what keeps the gauge about caches that have
	// something to be stale about.
	cache := coldCache(t, jwksServer(t, newSigner(t).jwks()).URL)

	if age, loaded := cache.Age(); loaded {
		t.Errorf("Age() = (%v, true) before any load, want (_, false)", age)
	}
}

func TestAWarmCacheReportsHowLongAgoItLoaded(t *testing.T) {
	cache := warmCache(t, newSigner(t))

	age, loaded := cache.Age()
	if !loaded {
		t.Fatal("Age() reports never loaded after a successful Refresh")
	}
	if age < 0 || age > time.Minute {
		t.Errorf("Age() = %v, want the time since the load that just happened", age)
	}
}

func TestAFailedRefreshLeavesTheAgeClimbing(t *testing.T) {
	// The clause the gauge exists for. Fail static keeps the old keys, so
	// nothing breaks and nothing is logged on the request path — the age is
	// the only thing that tells an operator the cache stopped being renewed.
	s := newSigner(t)
	srv := jwksServer(t, s.jwks())
	cache := coldCache(t, srv.URL)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	before, _ := cache.Age()
	srv.Close() // the issuer goes dark
	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh against a dead endpoint must report the failure")
	}

	after, loaded := cache.Age()
	if !loaded {
		t.Fatal("a failed refresh cleared the load timestamp — fail static must keep it")
	}
	if after < before {
		t.Errorf("Age() went %v → %v across a failed refresh, want it to keep climbing — a failure must never look like a fresh load", before, after)
	}
}
