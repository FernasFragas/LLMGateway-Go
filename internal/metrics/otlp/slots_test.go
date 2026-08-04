package otlp

import (
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/slots"
)

func TestRegisterSlotsReportsInFlightPerAppIncludingIdleOnes(t *testing.T) {
	mp, reader := meterAndReader()

	limiter := slots.New(800, map[string]int{"rag-api": 600, "agent-service": 300})
	release, _, ok := limiter.TryAcquire("rag-api")
	if !ok {
		t.Fatal("TryAcquirerag-api: want a grant on an empty limiter")
	}
	t.Cleanup(release)

	if err := RegisterSlots(mp.Meter("test"), limiter, []string{"rag-api", "agent-service"}); err != nil {
		t.Fatalf("RegisterSlots: %v", err)
	}

	got := collected(t, reader)
	values := got["in_flight"]
	if len(values) != 2 {
		t.Fatalf("in_flight data points = %d, want 2 — one per configured app, idle or not", len(values))
	}

	var sawOccupied, sawIdle bool
	for _, v := range values {
		switch v {
		case 1:
			sawOccupied = true
		case 0:
			sawIdle = true
		}
	}
	if !sawOccupied || !sawIdle {
		t.Errorf("in_flight values = %v, want one app at 1 (rag-api) and one at 0 (agent-service, idle but still reported)", values)
	}
}
