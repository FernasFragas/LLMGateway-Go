package secrets

import (
	"context"
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

// failing serves the keys it knows and errors for every other path.
type failing struct{ keys map[string]string }

func (f *failing) Fetch(_ context.Context, path string) (string, error) {
	if key, ok := f.keys[path]; ok {
		return key, nil
	}

	return "", errSourceDown
}
