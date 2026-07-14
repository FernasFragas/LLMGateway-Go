package gateway

import (
	"context"
	"time"
)

// This file is the boundary of the hexagon. The core owns these contracts;
// adapters (internal/auth, internal/api, internal/metrics, provider clients)
// implement them and translate between the domain's terms and one
// technology's terms. Adapters import this package — never the reverse.

// AppDirectory resolves an API key to the app that owns it. Implementations
// must answer from local state (fail static, decision #1): a slow or absent
// secret source must never reach the request path. A cold instance with no
// keys refuses readiness instead — that failure mode lives at the probe, not
// here.
type AppDirectory interface {
	AppForKey(ctx context.Context, apiKey string) (App, bool)
}

// RateDecision is a rate limiter's answer for one request.
type RateDecision struct {
	Allowed    bool
	RetryAfter time.Duration // when to try again; set when Allowed is false
	Quota      *QuotaDetail  // the window that refused; set when Allowed is false
}

// RateLimiter meters the rate currencies — requests and tokens per window —
// per app. A returned error means the limiter itself failed (e.g. Redis
// down); the core then fails open (decision #1): quota goes temporarily
// unenforced rather than unavailable, and the degradation is recorded.
type RateLimiter interface {
	Allow(ctx context.Context, app string) (RateDecision, error)
}

// SlotLimiter meters the third currency: in-flight slots (decision #7).
// Rate quotas alone don't isolate — an app can sit far under its rps quota
// while holding hundreds of slots for minutes. Implementations enforce the
// per-app ceiling and the global cap; which one refused is reported so the
// caller can be told.
type SlotLimiter interface {
	// TryAcquire claims a slot for app, never blocking: queueing inside the
	// gateway would hide starvation instead of pricing it. When the app is
	// at a ceiling it returns ok = false and the ceiling that refused.
	// release must be called exactly once when the request finishes.
	TryAcquire(app string) (release func(), ceiling int, ok bool)
}

// ProviderClient executes one chat completion attempt against one model
// provider.
// Adapters translate the domain request into their provider's wire format
// and report failures as *ProviderFault, so the core can classify outcomes
// without knowing any provider's protocol. The context carries the per-try
// deadline; adapters must abort the outbound call when it fires.
type ProviderClient interface {
	Complete(ctx context.Context, mp ModelProvider, req ChatRequest) (Completion, error)
}

// UsageRecorder receives the facts the observability stack turns into
// metrics: token spend, rejections, and the spend the gateway provably
// cannot observe (decision #6). Implementations must never block or fail
// the data path — observability never gates it.
type UsageRecorder interface {
	// RecordCompletion attributes one served request: tokens and latency by
	// app and model provider, and whether a failover served it.
	RecordCompletion(app string, mp ModelProvider, usage Usage, latency time.Duration, failedOver bool)
	// RecordRejection counts a request the gateway refused, by outcome code.
	RecordRejection(app string, code ErrorCode)
	// RecordRateLimiterFailOpen counts a request admitted unmetered because
	// the limiter itself was down.
	RecordRateLimiterFailOpen(app string)
	// RecordDoubleSpendRisk counts a failover after a timeout or garbage
	// 200, with the upper-bound tokens the abandoned attempt may still bill.
	RecordDoubleSpendRisk(app string, mp ModelProvider, estimatedTokens int)
	// RecordClientDisconnect counts a caller that vanished mid-request, with
	// the upper-bound tokens the aborted attempt may still bill.
	RecordClientDisconnect(app string, mp ModelProvider, estimatedTokens int)
}

// NopUsageRecorder discards everything; the default when no recorder is wired.
type NopUsageRecorder struct{}

func (NopUsageRecorder) RecordCompletion(string, ModelProvider, Usage, time.Duration, bool) {}
func (NopUsageRecorder) RecordRejection(string, ErrorCode)                                  {}
func (NopUsageRecorder) RecordRateLimiterFailOpen(string)                                   {}
func (NopUsageRecorder) RecordDoubleSpendRisk(string, ModelProvider, int)                   {}
func (NopUsageRecorder) RecordClientDisconnect(string, ModelProvider, int)                  {}
