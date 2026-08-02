package auth

import (
	"context"
	"time"
)

// Refresher is the background key refresh as KeepFresh drives it — declared
// here because this package is the caller. The JWKS cache satisfies it, and
// so does whatever main composes around the cache, which is how every
// attempt reaches the logs decorator without this loop knowing one exists.
type Refresher interface {
	Refresh(ctx context.Context) error
}

// KeepFresh keeps r warm until ctx ends: once immediately, so readiness turns
// green as soon as the issuer answers, then every interval. How often the
// gateway re-reads the cluster's signing keys is this package's policy, not
// main's — main builds the decorated refresher and hands it over.
//
// Errors are dropped on purpose: a failed refresh keeps the previous keys
// (fail static), the decorator gives the failure its line, and this loop's
// one job is making sure the next attempt is always coming.
func KeepFresh(ctx context.Context, r Refresher, interval time.Duration) {
	_ = r.Refresh(ctx)
	if interval <= 0 {
		return
	}

	tick := time.NewTicker(interval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = r.Refresh(ctx)
		}
	}
}
