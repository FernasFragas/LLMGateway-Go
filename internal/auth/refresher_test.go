package auth

// The background refresh loop is authentication logic: how often the gateway
// re-reads the cluster's signing keys is this package's policy, not main's.
// main wires the decorator around the cache and hands the result to KeepFresh
// in one line; everything the loop knows lives here, behind the Refresher
// port — so what it drives is whatever main composed, decorators included.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestKeepFreshRefreshesOnceBeforeTheFirstTick(t *testing.T) {
	// Readiness turns green as soon as the issuer answers — a loop that waited
	// a full interval would hold a healthy pod out of rotation for no reason.
	r := &counting{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	keepingFresh(ctx, r, time.Hour)

	eventually(t, func() bool { return r.count() == 1 },
		"the loop must refresh immediately, not wait out the first interval")
}

func TestKeepFreshKeepsRefreshingOnItsCadence(t *testing.T) {
	r := &counting{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	keepingFresh(ctx, r, time.Millisecond)

	eventually(t, func() bool { return r.count() >= 3 },
		"the loop must keep driving refreshes, interval after interval")
}

func TestAFailedRefreshNeverStopsTheLoop(t *testing.T) {
	// Fail static only holds if the loop outlives the outage: the stale keys
	// stay servable precisely because the next attempt is always coming.
	r := &counting{err: errors.New("issuer unreachable")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	keepingFresh(ctx, r, time.Millisecond)

	eventually(t, func() bool { return r.count() >= 3 },
		"an error must be observed and survived, never a reason to exit")
}

func TestKeepFreshReturnsWhenTheContextEnds(t *testing.T) {
	r := &counting{}
	ctx, cancel := context.WithCancel(context.Background())

	done := keepingFresh(ctx, r, time.Millisecond)
	cancel()

	eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, "shutdown must end the loop — a leaked goroutine outlives the drain")
}

// keepingFresh runs KeepFresh on its own goroutine, as main does, returning a
// channel that closes when the loop exits.
func keepingFresh(ctx context.Context, r Refresher, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		KeepFresh(ctx, r, interval)
	}()

	return done
}

// counting is a Refresher recording how many times the loop drove it.
type counting struct {
	mu  sync.Mutex
	n   int
	err error
}

func (c *counting) Refresh(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++

	return c.err
}

func (c *counting) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.n
}

// eventually polls until want holds or the deadline passes — the loop is
// asynchronous by design, so there is nothing to synchronize on.
func eventually(t *testing.T, want func() bool, because string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if want() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition never held: %s", because)
}
