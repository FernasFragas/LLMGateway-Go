package gateway

// The token-rate currency (ADR-003): what a spent budget refuses, what a
// served completion pays back, and what the gateway deliberately never
// charges for.

import (
	"testing"
	"time"
)

func TestAnAppOverItsTokenBudgetIsRefusedBeforeAnyProviderIsCalled(t *testing.T) {
	f := newGateway(t, tokenBudgetSpent(42*time.Second, QuotaDetail{Limit: 200000, WindowSeconds: 60, Used: 200512}))

	_, err := f.chat("gpt-4.1")

	gwErr := wantErrorCode(t, err, CodeQuotaExceeded)
	if gwErr.RetryAfter != 42*time.Second {
		t.Errorf("RetryAfter = %v, want the window's own reset so the caller knows when to return", gwErr.RetryAfter)
	}
	if gwErr.Quota == nil || gwErr.Quota.WindowSeconds != 60 {
		t.Errorf("Quota = %+v, want the 60s window that tells a token refusal from an rps one", gwErr.Quota)
	}
	if len(f.provider.calls) != 0 {
		t.Errorf("called %v, want a refused request never forwarded", f.provider.calls)
	}
}

func TestTheRequestQuotaIsCheckedBeforeTheTokenBudget(t *testing.T) {
	// The rps verdict is cheaper and exact, so a request refused on requests
	// must never cost a token read.
	f := newGateway(t,
		quotaDenied(time.Second, QuotaDetail{Limit: 5, WindowSeconds: 1, Used: 6}),
		tokenBudgetSpent(time.Minute, QuotaDetail{Limit: 200000, WindowSeconds: 60, Used: 200000}),
	)

	_, err := f.chat("gpt-4.1")

	gwErr := wantErrorCode(t, err, CodeQuotaExceeded)
	if gwErr.Quota == nil || gwErr.Quota.WindowSeconds != 1 {
		t.Errorf("Quota = %+v, want the rps window — it refuses first", gwErr.Quota)
	}
}

func TestAServedCompletionIsDebitedAtItsActualCost(t *testing.T) {
	f := newGateway(t)

	if _, err := f.chat("gpt-4.1"); err != nil {
		t.Fatalf("chat: %v", err)
	}

	// The scripted provider reports 21 prompt + 30 completion tokens.
	if len(f.tokens.debits) != 1 || f.tokens.debits[0] != 51 {
		t.Errorf("debits = %v, want one debit of the 51 tokens the completion actually cost", f.tokens.debits)
	}
}

func TestATokenLimiterOutageAdmitsTheRequestAndRecordsIt(t *testing.T) {
	// Fail open, exactly as the request-rate limiter does (decision #1):
	// unmetered is a state the operator must see, never a silent default.
	f := newGateway(t, tokenLimiterDown())

	if _, err := f.chat("gpt-4.1"); err != nil {
		t.Fatalf("chat: %v", err)
	}

	if f.rec.failOpens != 1 {
		t.Errorf("failOpens = %d, want the unenforced budget recorded", f.rec.failOpens)
	}
}

func TestALostDebitNeverFailsARequestTheProviderAlreadyServed(t *testing.T) {
	f := newGateway(t, debitsFailing())

	result, err := f.chat("gpt-4.1")

	if err != nil {
		t.Fatalf("a failed debit refused an answer the provider had already billed for: %v", err)
	}
	if result.Message.Content == "" {
		t.Error("the caller lost a real completion to an accounting write")
	}
}

func TestAFailedRequestIsNeverDebited(t *testing.T) {
	// The gateway holds an upper-bound estimate for an abandoned attempt, not
	// a fact — and an estimate that refuses real traffic is worse than a
	// budget that runs loose. Unobserved spend is priced separately.
	f := newGateway(t,
		modelProviders(gptOpenAI),
		failing("openai", FaultServerError),
	)

	if _, err := f.chat("gpt-4.1"); err == nil {
		t.Fatal("want the upstream failure to surface")
	}

	if len(f.tokens.debits) != 0 {
		t.Errorf("debits = %v, want nothing charged for an attempt that served no one", f.tokens.debits)
	}
}

func TestADisconnectedCallerIsNeverDebited(t *testing.T) {
	f := newGateway(t, modelProviders(gptOpenAI), disconnectingDuring("openai"))

	if _, err := f.chat("gpt-4.1"); err == nil {
		t.Fatal("want the disconnect to surface")
	}

	if len(f.tokens.debits) != 0 {
		t.Errorf("debits = %v, want an abandoned attempt left to the unobserved-spend estimate", f.tokens.debits)
	}
}

func TestOnlyTheServingAttemptIsDebitedAfterAFailover(t *testing.T) {
	f := newGateway(t, failing("openai", FaultServerError))

	result, err := f.chat("gpt-4.1")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if result.Provider == "openai" {
		t.Fatal("expected the failover to serve")
	}

	if len(f.tokens.debits) != 1 || f.tokens.debits[0] != 51 {
		t.Errorf("debits = %v, want only the attempt that answered charged — the abandoned one is unobserved spend", f.tokens.debits)
	}
}
