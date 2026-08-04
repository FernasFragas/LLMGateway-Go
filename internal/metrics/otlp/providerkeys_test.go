package otlp

import (
	"context"
	"testing"

	keysmetrics "github.com/FernasFragas/LLMGateway-Go/internal/metrics/providerkeys"
	"github.com/FernasFragas/LLMGateway-Go/internal/providerkeys"
)

func TestRegisterProviderKeysReportsRefreshesTriggersAndAge(t *testing.T) {
	mp, reader := meterAndReader()

	fetch := stubFetcher{key: "sk-live"}
	cache, err := providerkeys.New(fetch, map[string]providerkeys.Source{"openai": {Path: "secret/openai"}})
	if err != nil {
		t.Fatalf("providerkeys.New: %v", err)
	}
	refresher := keysmetrics.NewRefresher(cache)
	cache.RefreshVia(refresher)
	// Through refresher, not cache.Refresh directly: RefreshVia only
	// redirects the cache's own background paths, so calling the metrics
	// decorator is what a real refresh (periodic or triggered) actually does.
	if err := refresher.Refresh(context.Background(), "openai"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	triggers := keysmetrics.NewTriggerCounter()
	triggers.Trigger("openai")

	if err := RegisterProviderKeys(mp.Meter("test"), refresher, triggers, cache, []string{"openai"}); err != nil {
		t.Fatalf("RegisterProviderKeys: %v", err)
	}

	got := collected(t, reader)
	if v := got["provider_key_refresh_total"]; len(v) != 1 || v[0] != 1 {
		t.Errorf("provider_key_refresh_total = %v, want [1]", v)
	}
	if v := got["provider_key_refresh_triggered_total"]; len(v) != 1 || v[0] != 1 {
		t.Errorf("provider_key_refresh_triggered_total = %v, want [1]", v)
	}

	ages := collectedFloat(t, reader)
	v, ok := ages["provider_key_age_seconds"]
	if !ok || len(v) != 1 || v[0] < 0 {
		t.Errorf("provider_key_age_seconds = %v, want one non-negative reading — a loaded credential's age must be visible", v)
	}
}

func TestRegisterProviderKeysSkipsProvidersThatNeverLoaded(t *testing.T) {
	mp, reader := meterAndReader()

	cache, err := providerkeys.New(stubFetcher{}, map[string]providerkeys.Source{"anthropic": {Path: "secret/anthropic"}})
	if err != nil {
		t.Fatalf("providerkeys.New: %v", err)
	}
	refresher := keysmetrics.NewRefresher(cache)
	triggers := keysmetrics.NewTriggerCounter()

	// A cold cache — Age reports (0, false) for anthropic — must not make
	// registration itself fail; the gauge callback just observes nothing.
	if err := RegisterProviderKeys(mp.Meter("test"), refresher, triggers, cache, []string{"anthropic"}); err != nil {
		t.Fatalf("RegisterProviderKeys on a cold cache: %v", err)
	}

	if v, ok := collectedFloat(t, reader)["provider_key_age_seconds"]; ok {
		t.Errorf("provider_key_age_seconds = %v, want no reading for a provider that never loaded", v)
	}
}

type stubFetcher struct {
	key string
	err error
}

func (f stubFetcher) Fetch(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.key == "" {
		return "sk-default", nil
	}
	return f.key, nil
}
