package logs

import "testing"

func TestSlotRefusalLogsTheCeilingThatRefused(t *testing.T) {
	log, out := captured(t)
	sl := NewSlotLimiter(slots{full: true, ceiling: 300}, log)

	_, ceiling, ok := sl.TryAcquire("rag-api")

	if ok || ceiling != 300 {
		t.Fatalf("refusal = (ceiling=%d, ok=%v), want the inner limiter's 300, refused", ceiling, ok)
	}
	wantLogged(t, out, "rag-api", "300")
}

func TestGrantedSlotPassesThroughSilently(t *testing.T) {
	log, out := captured(t)
	sl := NewSlotLimiter(slots{}, log)

	release, _, ok := sl.TryAcquire("rag-api")

	if !ok || release == nil {
		t.Fatal("the grant did not pass through — release must reach the core intact")
	}
	wantSilence(t, out)
}
