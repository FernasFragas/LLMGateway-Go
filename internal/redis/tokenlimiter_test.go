package redis

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAnAppWithBudgetLeftIsAdmitted(t *testing.T) {
	store := fakeRedis(t)
	l := tokenLimiterAt(t, store, map[string]int{"rag-api": 1000})

	if err := l.Settle(context.Background(), "rag-api", 999); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	if d, err := l.Check(context.Background(), "rag-api"); err != nil || !d.Allowed {
		t.Errorf("an app one token under its budget was refused: %+v %v", d, err)
	}
}

func TestAnAppIsRefusedOnceItsBudgetIsSpent(t *testing.T) {
	// "At or above", not "above": the debit lands after the response, so the
	// only question a check can honestly ask is whether the minute is spent.
	store := fakeRedis(t)
	l := tokenLimiterAt(t, store, map[string]int{"rag-api": 1000})

	if err := l.Settle(context.Background(), "rag-api", 1000); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	d, err := l.Check(context.Background(), "rag-api")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Fatal("an app that has spent its whole budget was admitted")
	}
	if d.RetryAfter <= 0 || d.RetryAfter > tokenWindow {
		t.Errorf("RetryAfter = %v, want the time left in the window so the caller is told when to return", d.RetryAfter)
	}
	if d.Quota == nil || d.Quota.Limit != 1000 || d.Quota.Used != 1000 || d.Quota.WindowSeconds != 60 {
		t.Errorf("Quota = %+v, want the budget, the spend, and the 60s window that distinguishes it from rps", d.Quota)
	}
}

func TestInFlightTokensAreInvisibleToTheCheck(t *testing.T) {
	// The documented soft ceiling: nothing is reserved, so an app under budget
	// is admitted no matter how much is already running. Overshoot is bounded
	// by the slot ceiling, not by this limiter (ADR-003).
	store := fakeRedis(t)
	l := tokenLimiterAt(t, store, map[string]int{"rag-api": 1000})

	for range 20 {
		if d, err := l.Check(context.Background(), "rag-api"); err != nil || !d.Allowed {
			t.Fatalf("a request was refused on tokens that had not settled yet: %+v %v", d, err)
		}
	}
}

func TestOneAppsBudgetNeverTouchesAnother(t *testing.T) {
	store := fakeRedis(t)
	l := tokenLimiterAt(t, store, map[string]int{"rag-api": 100, "agent-service": 100})

	_ = l.Settle(context.Background(), "rag-api", 100)
	if d, _ := l.Check(context.Background(), "rag-api"); d.Allowed {
		t.Fatal("rag-api should be over budget")
	}

	if d, err := l.Check(context.Background(), "agent-service"); err != nil || !d.Allowed {
		t.Errorf("agent-service refused because of rag-api's spend: %+v %v", d, err)
	}
}

func TestANewWindowStartsTheBudgetOver(t *testing.T) {
	store := fakeRedis(t)
	l := tokenLimiterAt(t, store, map[string]int{"rag-api": 100})
	at := time.Unix(600, 0)
	l.now = func() time.Time { return at }

	_ = l.Settle(context.Background(), "rag-api", 100)
	if d, _ := l.Check(context.Background(), "rag-api"); d.Allowed {
		t.Fatal("a spent budget should refuse within its own window")
	}

	at = at.Add(tokenWindow)

	if d, err := l.Check(context.Background(), "rag-api"); err != nil || !d.Allowed {
		t.Errorf("the next window did not start clean: %+v %v", d, err)
	}
}

func TestADebitLandsInTheWindowItsCheckRead(t *testing.T) {
	store := fakeRedis(t)
	l := tokenLimiterAt(t, store, map[string]int{"rag-api": 500})
	at := time.Unix(600, 0)
	l.now = func() time.Time { return at }

	if _, err := l.Check(context.Background(), "rag-api"); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := l.Settle(context.Background(), "rag-api", 51); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	if got := store.spend(l.key("rag-api", at)); got != 51 {
		t.Errorf("window counter = %d, want the 51 tokens the completion cost", got)
	}
}

func TestTheBudgetIsSentWithATTLSoItIsNeverPermanent(t *testing.T) {
	// Without the expiry an app that spent its budget once would stay refused
	// forever — the failure INCRBY and EXPIRE share a script to prevent.
	store := fakeRedis(t)
	l := tokenLimiterAt(t, store, map[string]int{"rag-api": 100})

	if err := l.Settle(context.Background(), "rag-api", 10); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	sent := store.lastCommand()
	if len(sent) == 0 || !strings.Contains(sent[1], "EXPIRE") {
		t.Fatalf("script = %q, want it to set an expiry alongside the debit", sent)
	}
	if ttl := sent[len(sent)-1]; ttl == "" || ttl == "0" {
		t.Errorf("ttl argument = %q, want a positive lifetime", ttl)
	}
}

func TestAnUnreachableStoreLeavesTheBudgetUnenforced(t *testing.T) {
	// The core fails open on this error, exactly as it does for rps. The
	// limiter's job is to say the store failed, never to decide what it means.
	l := NewTokenLimiter(deadStore(t), map[string]int{"rag-api": 100})

	d, err := l.Check(context.Background(), "rag-api")
	if err == nil {
		t.Fatal("a dead store must be reported, so the core can fail open and record the degradation")
	}
	if d.Allowed {
		t.Error("the limiter must not decide to admit; that call belongs to the core")
	}
}

func TestALostDebitIsReportedRatherThanSwallowed(t *testing.T) {
	// The core drops this error — the caller already has a real answer — but
	// it must reach the core to be logged and counted, or the under-debit is
	// invisible, which is the one thing the design forbids.
	l := NewTokenLimiter(deadStore(t), map[string]int{"rag-api": 100})

	if err := l.Settle(context.Background(), "rag-api", 51); err == nil {
		t.Error("a failed debit reported success; the loss would never be counted")
	}
}

func TestAZeroBudgetMeansUnmeteredAndNeverCallsTheStore(t *testing.T) {
	store := fakeRedis(t)
	l := tokenLimiterAt(t, store, map[string]int{"declared": 0})

	for range 50 {
		if d, err := l.Check(context.Background(), "declared"); err != nil || !d.Allowed {
			t.Fatalf("a zero budget refused a request; zero means unmetered: %+v %v", d, err)
		}
		if err := l.Settle(context.Background(), "declared", 5000); err != nil {
			t.Fatalf("Settle: %v", err)
		}
	}
	if d, err := l.Check(context.Background(), "never-configured"); err != nil || !d.Allowed {
		t.Fatalf("an app with no entry was metered: %+v %v", d, err)
	}
	if got := store.commands(); got != 0 {
		t.Errorf("%d commands sent for unmetered apps, want none — an unmetered app should not cost a round trip", got)
	}
}

func TestAZeroTokenCompletionCostsNoRoundTrip(t *testing.T) {
	store := fakeRedis(t)
	l := tokenLimiterAt(t, store, map[string]int{"rag-api": 100})

	if err := l.Settle(context.Background(), "rag-api", 0); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if got := store.commands(); got != 0 {
		t.Errorf("%d commands sent to debit nothing, want none", got)
	}
}

func TestAStoreErrorReplyTravelsFromTheCheck(t *testing.T) {
	store := fakeRedis(t)
	store.replyErr = "LOADING Redis is loading the dataset in memory"
	l := tokenLimiterAt(t, store, map[string]int{"rag-api": 100})

	if _, err := l.Check(context.Background(), "rag-api"); err == nil ||
		!strings.Contains(err.Error(), "LOADING") {
		t.Errorf("error = %v, want the store's own message carried through", err)
	}
}
