package metrics

import "sync"

// TriggerCounter counts out-of-band refreshes actually started per
// provider — main bumps it from the same callback that already learns
// TriggerRefresh's bool. It answers the question the refresh counter alone
// cannot: whether a rotation is healing itself (triggers stay rare) or a
// misconfiguration is looping (triggers track request volume).
type TriggerCounter struct {
	mu        sync.Mutex
	triggered map[string]int64
}

// NewTriggerCounter returns an empty counter, ready to use.
func NewTriggerCounter() *TriggerCounter {
	return &TriggerCounter{triggered: make(map[string]int64)}
}

// Trigger records one started out-of-band refresh for provider.
func (c *TriggerCounter) Trigger(provider string) {
	c.mu.Lock()
	c.triggered[provider]++
	c.mu.Unlock()
}

// Triggered reports how many out-of-band refreshes started for provider.
func (c *TriggerCounter) Triggered(provider string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.triggered[provider]
}
