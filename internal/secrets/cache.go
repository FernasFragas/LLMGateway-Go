package secrets

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/shared"
)

// fetchTimeout bounds one credential read. It is deliberately independent of
// any RefreshInterval: a hung fetch would otherwise hold its provider's
// single-flight slot and silence the out-of-band trigger indefinitely
// (ADR-001).
const fetchTimeout = 10 * time.Second

// triggerCooldown is the minimum gap between two out-of-band refreshes of the
// same provider. Without it a permanently-rejected key turns request rate
// into load against the secret store — one revoked credential becoming a
// self-inflicted denial of service (decision #8).
const triggerCooldown = 30 * time.Second

// Fetcher reads one credential from wherever it lives. Kind picks the
// implementation; everything above this line is identical for both.
type Fetcher interface {
	Fetch(ctx context.Context, path string) (string, error)
}

// Source is one provider's credential location and its own refresh cadence.
type Source struct {
	Path            string
	RefreshInterval time.Duration
}

// entry is one provider's slot: the credential in force, when it was last
// loaded, and the guards that keep the out-of-band trigger from stampeding.
type entry struct {
	source Source

	mu          sync.RWMutex
	key         string
	loadedAt    time.Time
	refreshing  bool
	lastTrigger time.Time
}

// Cache holds every configured provider's credential in memory. The zero
// value is not usable; call New.
type Cache struct {
	fetch   Fetcher
	entries map[string]*entry
	now     func() time.Time // swapped in tests
}

// New builds the cache over sources, keyed by provider name — the one
// provider identity the rest of the architecture uses (routes[].provider and
// failover_order). A provider absent from sources needs no credential and is
// not an error.
func New(fetch Fetcher, sources map[string]Source) (*Cache, error) {
	if fetch == nil && len(sources) > 0 {
		return nil, errors.New("secrets: a Fetcher is required when any provider has a source")
	}

	entries := make(map[string]*entry, len(sources))
	for provider, src := range sources {
		if src.Path == "" {
			return nil, fmt.Errorf("secrets: provider %q has no path", provider)
		}
		entries[provider] = &entry{source: src}
	}

	return &Cache{fetch: fetch, entries: entries, now: time.Now}, nil
}

// KeyFor returns the credential in force for provider, or "" when it has none
// configured and when nothing has loaded yet. Empty is a valid answer, not a
// failure: the adapter sends no auth header, and a provider that genuinely
// needs one learns so from the upstream 401 that follows.
func (c *Cache) KeyFor(provider string) string {
	e, ok := c.entries[provider]
	if !ok {
		return ""
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.key
}

// Key hands an adapter its accessor. It is resolved per request, so a
// rotation reaches the wire without a restart.
func (c *Cache) Key(provider string) shared.Key {
	return func() string { return c.KeyFor(provider) }
}

// Providers lists the configured provider names, sorted so callers that log
// or schedule over them stay deterministic.
func (c *Cache) Providers() []string {
	names := make([]string, 0, len(c.entries))
	for provider := range c.entries {
		names = append(names, provider)
	}
	sort.Strings(names)

	return names
}

// Interval reports the cadence configured for provider; zero when it has no
// source. main's refresh loop reads it rather than inventing its own.
func (c *Cache) Interval(provider string) time.Duration {
	e, ok := c.entries[provider]
	if !ok {
		return 0
	}

	return e.source.RefreshInterval
}

// Age reports how long ago provider's credential was loaded, and whether it
// ever has. It is what the staleness gauge reports — fail static is only
// defensible because the staleness is visible.
func (c *Cache) Age(provider string) (time.Duration, bool) {
	e, ok := c.entries[provider]
	if !ok {
		return 0, false
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.loadedAt.IsZero() {
		return 0, false
	}

	return c.now().Sub(e.loadedAt), true
}

// Refresh loads one provider's credential, replacing the cached value only on
// success: a failed read keeps the last known good one (fail static), and the
// error still travels so the decorator can log it and the caller can count it.
func (c *Cache) Refresh(ctx context.Context, provider string) error {
	e, ok := c.entries[provider]
	if !ok {
		return fmt.Errorf("secrets: provider %q has no configured source", provider)
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	key, err := c.fetch.Fetch(ctx, e.source.Path)
	if err != nil {
		return fmt.Errorf("secrets: refresh %s: %w", provider, err)
	}
	if key == "" {
		// An empty read is a source that exists but holds nothing — almost
		// always a half-written secret. Overwriting a working credential with
		// it would turn a fixable mistake into an outage.
		return fmt.Errorf("secrets: refresh %s: source holds no credential", provider)
	}

	e.mu.Lock()
	e.key, e.loadedAt = key, c.now()
	e.mu.Unlock()

	return nil
}

// RefreshAll loads every configured provider, reporting the failures together
// rather than stopping at the first: one unreachable path must not hide the
// state of the others, and one provider's failure never blocks another's
// success.
func (c *Cache) RefreshAll(ctx context.Context) error {
	var errs []error
	for _, provider := range c.Providers() {
		if err := c.Refresh(ctx, provider); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// TriggerRefresh schedules an out-of-band refresh after a provider rejected
// the credential (decision #8). It never blocks the caller — the rejected
// request has already failed over — and it never runs more than one refresh
// per provider at a time, no more often than triggerCooldown. It reports
// whether a refresh was actually started, which is what the counter records:
// a rotation healing itself looks nothing like a misconfiguration looping.
func (c *Cache) TriggerRefresh(provider string) bool {
	e, ok := c.entries[provider]
	if !ok {
		return false
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.refreshing || c.now().Sub(e.lastTrigger) < triggerCooldown {
		return false
	}
	e.refreshing, e.lastTrigger = true, c.now()

	go func() {
		defer func() {
			e.mu.Lock()
			e.refreshing = false
			e.mu.Unlock()
		}()
		// Deliberately not the request's context: that request is already
		// finished, and its cancellation must not abort a refresh every later
		// request depends on.
		_ = c.Refresh(context.Background(), provider)
	}()

	return true
}

// Ready is health.Check-shaped. It reports ready once anything is servable:
// a gateway with no configured sources has nothing to wait for, and one whose
// sources are configured is ready as soon as any of them has loaded. Demanding
// that every source load would be worse than the fault it guards — replicas
// share a secret store, so there would be no healthy instance to shed to, and
// one unreachable path would ground the gateway for the providers that are
// fine.
func (c *Cache) Ready(context.Context) error {
	if len(c.entries) == 0 {
		return nil
	}

	for _, e := range c.entries {
		e.mu.RLock()
		loaded := !e.loadedAt.IsZero()
		e.mu.RUnlock()

		if loaded {
			return nil
		}
	}

	return errors.New("provider-keys: no configured source has loaded yet")
}
