// Package slots meters the third currency: in-flight requests (decision #7).
// Rate quotas alone do not isolate — an app can sit far under its rps quota
// while holding hundreds of slots for minutes, which is exactly how a slow
// app starves a fast one.
//
// It is in-process on purpose, and that is the one thing to understand about
// it. The port it implements takes no context and returns no error, because
// nothing here crosses a network: a semaphore is cheap, exact, and cannot
// fail open. The consequence is that ceilings are per replica, not
// gateway-wide — which is what the global cap is for anyway, since its stated
// job is bounding *this process's* memory (800 slots × 512 KB bodies ≈ 400 MB
// worst case). Per-app ceilings still do their work inside each replica: no
// app can crowd out another on the pod they share.
//
// Nothing queues. A request over a ceiling is refused immediately with 429 +
// Retry-After, because waiting inside the gateway would hide the starvation
// instead of pricing it.
package slots

import "sync"

// Limiter hands out slots against a per-app ceiling and a global cap. The
// zero value is not usable; call New.
type Limiter struct {
	global   int            // 0: no global cap
	ceilings map[string]int // per app; absent or 0: no ceiling for that app

	mu       sync.Mutex
	inFlight map[string]int
	total    int
}

// New builds the limiter. A zero global, a zero ceiling, and an app with no
// entry all mean the same thing: that dimension is not metered. This is the
// convention the config already uses for global_max_in_flight — an app pays
// for what it configures, not for what it omits — and both limiters follow it
// so one caller cannot behave inconsistently across currencies.
func New(global int, ceilings map[string]int) *Limiter {
	own := make(map[string]int, len(ceilings))
	for app, ceiling := range ceilings {
		own[app] = ceiling
	}

	return &Limiter{global: global, ceilings: own, inFlight: make(map[string]int, len(ceilings))}
}

// TryAcquire claims a slot for app without ever blocking. On refusal it
// reports the ceiling that refused — the app's own, or the global cap — so
// the caller can tell the app which limit it met.
//
// The app's ceiling is checked before the global one deliberately: when both
// are exhausted, the app's own limit is the honest explanation, and the
// global cap is nobody's fault in particular.
func (l *Limiter) TryAcquire(app string) (release func(), ceiling int, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if own := l.ceilings[app]; own > 0 && l.inFlight[app] >= own {
		return nil, own, false
	}
	if l.global > 0 && l.total >= l.global {
		return nil, l.global, false
	}

	l.inFlight[app]++
	l.total++

	// sync.Once because a slot returned twice would inflate capacity
	// silently, and the ceiling would stop meaning anything long before
	// anyone noticed. The port says exactly once; this makes a caller's bug
	// cost nothing.
	var once sync.Once

	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()

			l.inFlight[app]--
			if l.inFlight[app] <= 0 {
				delete(l.inFlight, app)
			}
			l.total--
		})
	}, 0, true
}

// InFlight reports how many slots app holds right now — the in_flight{app}
// gauge's source.
func (l *Limiter) InFlight(app string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.inFlight[app]
}

// Total reports the slots held across every app, against the global cap.
func (l *Limiter) Total() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.total
}
