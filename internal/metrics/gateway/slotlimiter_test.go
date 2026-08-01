package metrics

import "testing"

func TestGrantedSlotCountsAsAcquiredAndPassesReleaseThrough(t *testing.T) {
	sl := NewSlotLimiter(slots{})

	release, _, ok := sl.TryAcquire("rag-api")

	if !ok || release == nil {
		t.Fatal("the grant did not pass through — release must reach the core intact")
	}
	if sl.Acquired() != 1 || sl.Refused() != 0 {
		t.Errorf("acquired=%d refused=%d, want only the grant counted", sl.Acquired(), sl.Refused())
	}
}

func TestRefusedSlotCountsAsRefusedWithTheCeilingThatRefused(t *testing.T) {
	sl := NewSlotLimiter(slots{full: true, ceiling: 300})

	_, ceiling, ok := sl.TryAcquire("rag-api")

	if ok || ceiling != 300 {
		t.Fatalf("refusal = (ceiling=%d, ok=%v), want the inner limiter's 300, refused", ceiling, ok)
	}
	if sl.Refused() != 1 || sl.Acquired() != 0 {
		t.Errorf("acquired=%d refused=%d, want only the refusal counted", sl.Acquired(), sl.Refused())
	}
}
