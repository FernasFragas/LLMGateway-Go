package slots

import (
	"sync"
	"testing"
)

func TestAnAppIsRefusedAtItsOwnCeiling(t *testing.T) {
	l := New(0, map[string]int{"agent-service": 2})

	hold(t, l, "agent-service")
	hold(t, l, "agent-service")

	_, ceiling, ok := l.TryAcquire("agent-service")
	if ok {
		t.Fatal("a third slot was handed out over a ceiling of two")
	}
	if ceiling != 2 {
		t.Errorf("ceiling = %d, want the app's own limit reported back so it can be told which one it met", ceiling)
	}
}

func TestOneAppAtItsCeilingNeverBlocksAnother(t *testing.T) {
	// The anti-starvation rule, and the reason slots are metered at all.
	l := New(0, map[string]int{"agent-service": 1, "rag-api": 10})
	hold(t, l, "agent-service")

	if _, _, ok := l.TryAcquire("agent-service"); ok {
		t.Fatal("the saturated app must be refused")
	}
	if _, _, ok := l.TryAcquire("rag-api"); !ok {
		t.Error("the other app was refused because of its neighbour's load — that is the starvation this exists to prevent")
	}
}

func TestTheGlobalCapRefusesWhenNoAppHasBreachedItsOwn(t *testing.T) {
	// Ceilings are deliberately oversubscribed against the global cap: both
	// apps rarely peak together, and the cap settles the rare overlap.
	l := New(2, map[string]int{"a": 10, "b": 10})
	hold(t, l, "a")
	hold(t, l, "b")

	_, ceiling, ok := l.TryAcquire("a")
	if ok {
		t.Fatal("the global cap was exceeded")
	}
	if ceiling != 2 {
		t.Errorf("ceiling = %d, want the global cap reported — no app breached its own", ceiling)
	}
}

func TestTheAppsOwnCeilingIsTheReasonGivenWhenBothAreFull(t *testing.T) {
	// When both limits are exhausted the app's own is the honest explanation;
	// the global cap is nobody's fault in particular.
	l := New(1, map[string]int{"a": 1})
	hold(t, l, "a")

	if _, ceiling, _ := l.TryAcquire("a"); ceiling != 1 {
		t.Errorf("ceiling = %d, want the app's own limit named first", ceiling)
	}
}

func TestAReleasedSlotIsAvailableAgain(t *testing.T) {
	l := New(0, map[string]int{"a": 1})

	release, _, ok := l.TryAcquire("a")
	if !ok {
		t.Fatal("first slot refused")
	}
	release()

	if _, _, ok := l.TryAcquire("a"); !ok {
		t.Error("a released slot was never returned to the pool")
	}
}

func TestReleasingTwiceCannotInventCapacity(t *testing.T) {
	// A slot returned twice would inflate the ceiling silently, and the limit
	// would stop meaning anything long before anyone noticed.
	l := New(0, map[string]int{"a": 1})
	release, _, _ := l.TryAcquire("a")

	release()
	release()

	if got := l.InFlight("a"); got != 0 {
		t.Fatalf("in flight = %d, want 0", got)
	}
	hold(t, l, "a")
	if _, _, ok := l.TryAcquire("a"); ok {
		t.Error("the double release handed out a slot the ceiling does not allow")
	}
}

func TestZeroMeansUnmetered(t *testing.T) {
	// The convention config already uses for global_max_in_flight: an app
	// pays for what it configures, not for what it omits. "Zero requests
	// allowed" would make an omitted limits block silently deny everything.
	l := New(0, map[string]int{"declared": 0})

	for range 100 {
		if _, _, ok := l.TryAcquire("declared"); !ok {
			t.Fatal("a zero ceiling refused a request; zero means unmetered, not forbidden")
		}
	}
	for range 100 {
		if _, _, ok := l.TryAcquire("never-configured"); !ok {
			t.Fatal("an app with no entry was refused; absence means unmetered")
		}
	}
}

func TestConcurrentCallersNeverExceedTheCeiling(t *testing.T) {
	const ceiling, callers = 8, 200
	l := New(0, map[string]int{"a": ceiling})

	var wg sync.WaitGroup
	granted := make(chan func(), callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, _, ok := l.TryAcquire("a"); ok {
				granted <- release
			}
		}()
	}
	wg.Wait()
	close(granted)

	if got := len(granted); got != ceiling {
		t.Errorf("granted %d slots under contention, want exactly the ceiling of %d", got, ceiling)
	}
	if got := l.InFlight("a"); got != ceiling {
		t.Errorf("in flight = %d, want %d", got, ceiling)
	}
	for release := range granted {
		release()
	}
	if got := l.Total(); got != 0 {
		t.Errorf("total = %d after every release, want 0", got)
	}
}

// hold claims a slot the test does not intend to give back.
func hold(t *testing.T, l *Limiter, app string) {
	t.Helper()
	if _, _, ok := l.TryAcquire(app); !ok {
		t.Fatalf("%s was refused a slot it should have got", app)
	}
}
