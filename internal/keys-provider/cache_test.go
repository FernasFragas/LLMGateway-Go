package keys_provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestColdCacheRefusesReadinessAndServesNoKey(t *testing.T) {
	c := cacheOf(t, &serving{key: "sk-live"})

	if err := c.Ready(context.Background()); err == nil {
		t.Error("a cache that has never loaded must refuse readiness — the pod takes no traffic instead of calling providers with nothing")
	}
	if got := c.KeyFor("openai"); got != "" {
		t.Errorf("KeyFor = %q, want empty before the first load", got)
	}
}

func TestWarmCacheIsReadyAndServesItsKey(t *testing.T) {
	c := warm(t, &serving{key: "sk-live"})

	if err := c.Ready(context.Background()); err != nil {
		t.Errorf("Ready = %v, want nil once a source has loaded", err)
	}
	if got := c.KeyFor("openai"); got != "sk-live" {
		t.Errorf("KeyFor = %q, want the loaded credential", got)
	}
}

func TestNoConfiguredSourcesIsReadyImmediately(t *testing.T) {
	// Every route self-hosted: having nothing to load is a steady state, and
	// a gateway that waits for it would never start.
	c, err := New(nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Ready(context.Background()); err != nil {
		t.Errorf("Ready = %v, want nil — no sources means nothing to wait for", err)
	}
	if got := c.KeyFor("ollama"); got != "" {
		t.Errorf("KeyFor = %q, want empty for a provider that needs no credential", got)
	}
}

func TestFailedRefreshKeepsServingTheOldKey(t *testing.T) {
	src := &serving{key: "sk-live"}
	c := warm(t, src)

	src.set("", errSourceDown)

	if err := c.Refresh(context.Background(), "openai"); err == nil {
		t.Fatal("a failed refresh must still report its error — the decorator has nothing else to log")
	}
	if got := c.KeyFor("openai"); got != "sk-live" {
		t.Errorf("KeyFor = %q, want the last known good credential — fail static, never cold", got)
	}
	if err := c.Ready(context.Background()); err != nil {
		t.Errorf("Ready = %v, want a warm instance to stay ready through a source outage", err)
	}
}

func TestEmptyReadNeverOverwritesAWorkingKey(t *testing.T) {
	// A source that exists but holds nothing is a half-written secret. Taking
	// it at face value turns a fixable mistake into an outage.
	src := &serving{key: "sk-live"}
	c := warm(t, src)

	src.set("", nil)

	if err := c.Refresh(context.Background(), "openai"); err == nil {
		t.Fatal("an empty read must be refused, not cached")
	}
	if got := c.KeyFor("openai"); got != "sk-live" {
		t.Errorf("KeyFor = %q, want the credential that still works", got)
	}
}

func TestRotationReachesTheNextReader(t *testing.T) {
	src := &serving{key: "sk-first"}
	c := warm(t, src)
	key := c.Key("openai") // the accessor an adapter holds for its lifetime

	src.set("sk-rotated", nil)
	if err := c.Refresh(context.Background(), "openai"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if got := key(); got != "sk-rotated" {
		t.Errorf("adapter accessor = %q, want the rotated credential without rebuilding the adapter", got)
	}
}

func TestRefreshAllReportsEveryFailureNotJustTheFirst(t *testing.T) {
	c, err := New(&serving{err: errSourceDown}, map[string]Source{
		"openai":    {Path: "a", RefreshInterval: time.Minute},
		"anthropic": {Path: "b", RefreshInterval: time.Minute},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = c.RefreshAll(context.Background())
	if err == nil {
		t.Fatal("RefreshAll must report a total failure")
	}
	for _, want := range []string{"openai", "anthropic"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q — one dead source must not hide another's state", err, want)
		}
	}
}

func TestOneProvidersOutageLeavesTheOthersAlone(t *testing.T) {
	// Per-provider sourcing exists so blast radius stays at one provider.
	only := &failing{keys: map[string]string{"b": "sk-anthropic"}}
	c, err := New(only, map[string]Source{
		"openai":    {Path: "a", RefreshInterval: time.Minute},
		"anthropic": {Path: "b", RefreshInterval: time.Minute},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_ = c.RefreshAll(context.Background())

	if got := c.KeyFor("anthropic"); got != "sk-anthropic" {
		t.Errorf("anthropic key = %q, want it loaded despite openai's source failing", got)
	}
	if err := c.Ready(context.Background()); err != nil {
		t.Errorf("Ready = %v, want ready — one dead source degrades routing, it does not ground the pod", err)
	}
}

func TestTriggeredRefreshIsAsynchronousAndCooledDown(t *testing.T) {
	// Decision #8: an upstream 401 schedules a refresh off the request path,
	// and a permanently-rejected key must not turn request rate into load on
	// the secret store.
	src := &serving{key: "sk-first"}
	c := warm(t, src)
	before := src.count()

	if !c.TriggerRefresh("openai") {
		t.Fatal("the first rejection must schedule a refresh")
	}
	eventually(t, func() bool { return src.count() > before }, "the triggered refresh never ran")

	for i := 0; i < 50; i++ {
		if c.TriggerRefresh("openai") {
			t.Fatal("a second refresh ran inside the cooldown — one revoked key would become a DoS on the secret store")
		}
	}
}

func TestTriggeredRefreshNeverEvictsOnFailure(t *testing.T) {
	src := &serving{key: "sk-live"}
	c := warm(t, src)
	src.set("", errSourceDown)
	before := src.count()

	c.TriggerRefresh("openai")
	eventually(t, func() bool { return src.count() > before }, "the triggered refresh never ran")

	if got := c.KeyFor("openai"); got != "sk-live" {
		t.Errorf("KeyFor = %q, want the stale credential kept — sending none is strictly worse than sending an old one", got)
	}
}

func TestTriggerOnAnUnconfiguredProviderIsANoOp(t *testing.T) {
	c := warm(t, &serving{key: "sk-live"})

	if c.TriggerRefresh("ollama") {
		t.Error("a provider with no source has nothing to refresh — a 401 from it is not this cache's problem")
	}
}

func TestAgeIsUnsetUntilTheFirstLoad(t *testing.T) {
	src := &serving{key: "sk-live"}
	c := cacheOf(t, src)

	if _, loaded := c.Age("openai"); loaded {
		t.Error("age must report unloaded before the first read — the gauge would otherwise claim a fresh cache")
	}

	if err := c.RefreshAll(context.Background()); err != nil {
		t.Fatalf("RefreshAll: %v", err)
	}
	if _, loaded := c.Age("openai"); !loaded {
		t.Error("age must report loaded once a credential is in force")
	}
}

func TestTriggeredRefreshRunsThroughTheInjectedRefresher(t *testing.T) {
	// TriggerRefresh runs its refresh on a goroutine this cache owns. RefreshVia
	// is the seam that lets main hand that goroutine a decorated refresher —
	// without it, the refresh that follows an upstream 401 (decision #8) is the
	// one failure no decorator can ever observe.
	src := &serving{key: "sk-live"}
	c := warm(t, src)
	seen := &observing{next: c}
	c.RefreshVia(seen)

	if !c.TriggerRefresh("openai") {
		t.Fatal("the first rejection must schedule a refresh")
	}
	eventually(t, func() bool { return seen.calls() > 0 },
		"the out-of-band refresh must pass through the injected refresher — it is the decorator's only way in")
}

func TestInjectedRefresherObservesTheTriggeredFailure(t *testing.T) {
	// The failure the seam exists for: a rejection scheduled a refresh, the
	// source is down, and the decorator must see the source's own error.
	src := &serving{key: "sk-live"}
	c := warm(t, src)
	seen := &observing{next: c}
	c.RefreshVia(seen)
	src.set("", errSourceDown)

	c.TriggerRefresh("openai")

	eventually(t, func() bool { return seen.calls() > 0 }, "the triggered refresh never ran")
	if err := seen.lastErr(); !errors.Is(err, errSourceDown) {
		t.Errorf("observed error = %v, want the source's own — the seam must not launder it", err)
	}
}

func TestKeepFreshDrivesRefreshesThroughTheInjectedSeam(t *testing.T) {
	// The periodic loop is this cache's logic, not main's — and it must run
	// through the same seam TriggerRefresh does, or the decorator would see
	// only half the refreshes.
	src := &serving{key: "sk-live"}
	c, err := New(src, map[string]Source{
		"openai": {Path: "secret/data/llm/openai", RefreshInterval: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seen := &observing{next: c}
	c.RefreshVia(seen)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go c.KeepFresh(ctx, "openai")

	eventually(t, func() bool { return seen.calls() >= 3 },
		"the loop must keep driving refreshes on the provider's own cadence, through the seam")
}

func TestKeepFreshWithNoCadenceReturnsImmediately(t *testing.T) {
	// A zero interval means no background refresh is wanted; the loop has
	// nothing to do and must say so by returning, not by spinning or blocking.
	c := warm(t, &serving{key: "sk-live"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.KeepFresh(context.Background(), "unconfigured")
	}()

	eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, "a provider with no cadence must not hold a goroutine forever")
}

// failing serves the keys it knows and errors for every other path.
type failing struct{ keys map[string]string }

func (f *failing) Fetch(_ context.Context, path string) (string, error) {
	if key, ok := f.keys[path]; ok {
		return key, nil
	}

	return "", errSourceDown
}
