package metrics

import "testing"

func TestTriggerCountsPerProviderIndependently(t *testing.T) {
	c := NewTriggerCounter()

	c.Trigger("openai")
	c.Trigger("openai")
	c.Trigger("anthropic")

	if got := c.Triggered("openai"); got != 2 {
		t.Errorf("Triggered(openai) = %d, want 2", got)
	}
	if got := c.Triggered("anthropic"); got != 1 {
		t.Errorf("Triggered(anthropic) = %d, want 1", got)
	}
	if got := c.Triggered("ollama"); got != 0 {
		t.Errorf("Triggered(ollama) = %d, want 0 — a provider that never triggered must read as zero, not a missing key", got)
	}
}
