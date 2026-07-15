// Package gateway is the core of LLMGateway-Go: the business logic of
// admitting, routing, and failing over chat completions, written in domain
// vocabulary and free of transport and provider technology.
//
// The core owns the contract; technology applies for the job. The interfaces
// in ports.go are defined here, in the package that consumes them; adapters
// (HTTP API, provider clients, the Redis limiter, metrics) import this
// package and translate at the edges — never the reverse.
package gateway

import (
	"context"
	"errors"
	"fmt"
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

// Config is the core's own tuning — transport facts and budgets from the
// gateway config file, already parsed.
type Config struct {
	// ModelProviders is the registry of which endpoint serves which model.
	ModelProviders []ModelProvider
	// FailoverOrder is the provider order tried when an app's policy permits
	// leaving the requested model's own providers.
	FailoverOrder []string
	// TotalDeadline is the default per-request budget; apps may override it.
	TotalDeadline time.Duration
	// PerTryDeadline bounds each provider attempt so per-try waits can never
	// stack past the caller's budget (decision #2). Must be < TotalDeadline.
	PerTryDeadline time.Duration
}

// Deps are the adapters a Service drives, one per port.
type Deps struct {
	Apps        AppDirectory
	RateLimiter RateLimiter
	Slots       SlotLimiter
	Provider    ProviderClient
	Usage       UsageRecorder // optional; nil records nothing
}

// Service executes chat completions: one request in, one model call out,
// plus at most one failover. Everything else is a non-goal.
type Service struct {
	cfg      Config
	apps     AppDirectory
	limits   RateLimiter
	slots    SlotLimiter
	provider ProviderClient
	usage    UsageRecorder
}

func New(cfg Config, deps Deps) (*Service, error) {
	switch {
	case deps.Apps == nil:
		return nil, errors.New("gateway: AppDirectory is required")
	case deps.RateLimiter == nil:
		return nil, errors.New("gateway: RateLimiter is required")
	case deps.Slots == nil:
		return nil, errors.New("gateway: SlotLimiter is required")
	case deps.Provider == nil:
		return nil, errors.New("gateway: ProviderClient is required")
	case cfg.TotalDeadline <= 0:
		return nil, errors.New("gateway: TotalDeadline must be positive")
	case cfg.PerTryDeadline <= 0 || cfg.PerTryDeadline >= cfg.TotalDeadline:
		return nil, errors.New("gateway: PerTryDeadline must be positive and below TotalDeadline")
	}

	if deps.Usage == nil {
		deps.Usage = NopUsageRecorder{}
	}

	return &Service{
		cfg:      cfg,
		apps:     deps.Apps,
		limits:   deps.RateLimiter,
		slots:    deps.Slots,
		provider: deps.Provider,
		usage:    deps.Usage,
	}, nil
}

// A slot frees only when one of the app's own calls finishes, so the hint is
// a short constant — the point is pricing the wait, not predicting it.
const slotRetryAfter = time.Second

// maxAttempts is one try at the primary plus one failover (decision #2):
// never a second attempt at the same provider, never a chain of fallbacks
// eating the caller's budget.
const maxAttempts = 2

// Chat runs one completion through the whole contract: identify the app,
// meter its three currencies (requests, tokens, slots), route to the
// requested model, fail over per the app's declared policy, and disclose
// what actually served.
func (s *Service) Chat(ctx context.Context, apiKey string, req ChatRequest) (ChatResult, error) {
	app, ok := s.apps.AppForKey(ctx, apiKey)
	if !ok {
		return ChatResult{}, &Error{Code: CodeUnauthorized, Message: "missing or invalid API key"}
	}

	if err := req.Validate(); err != nil {
		s.usage.RecordRejection(app.Name, CodeInvalidRequest)
		return ChatResult{}, err
	}

	decision, err := s.limits.Allow(ctx, app.Name)
	switch {
	case err != nil:
		// The limiter itself is down. Fail open (decision #1): availability
		// over enforcement — and record it, because unmetered is a state the
		// operator must see, not a silent default.
		s.usage.RecordRateLimiterFailOpen(app.Name)
	case !decision.Allowed:
		s.usage.RecordRejection(app.Name, CodeQuotaExceeded)

		return ChatResult{}, &Error{
			Code:       CodeQuotaExceeded,
			Message:    fmt.Sprintf("%s is over its rate quota", app.Name),
			RetryAfter: decision.RetryAfter,
			Quota:      decision.Quota,
		}
	}

	release, ceiling, ok := s.slots.TryAcquire(app.Name)
	if !ok {
		s.usage.RecordRejection(app.Name, CodeConcurrencyCeiling)

		return ChatResult{}, &Error{
			Code:        CodeConcurrencyCeiling,
			Message:     fmt.Sprintf("%s is at its in-flight ceiling (%d)", app.Name, ceiling),
			RetryAfter:  slotRetryAfter,
			MaxInFlight: ceiling,
		}
	}
	defer release()

	eligible, constrained := buildModelsProvidersFallback(req.Model, app.Policy, s.cfg.ModelProviders, s.cfg.FailoverOrder)
	if len(eligible) == 0 {
		s.usage.RecordRejection(app.Name, CodeModelUnavailable)
		return ChatResult{}, modelUnavailable(req.Model)
	}

	callerCtx := ctx
	total := s.cfg.TotalDeadline
	if app.TotalDeadline > 0 {
		total = app.TotalDeadline
	}
	ctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()

	attempts := 0
	tried := make(map[string]bool, maxAttempts)
	for _, mp := range eligible {
		if attempts == maxAttempts {
			break
		}
		if tried[mp.Provider] {
			continue // never the same provider twice
		}
		tried[mp.Provider] = true
		attempts++

		start := time.Now()
		completion, err := s.tryModelProvider(ctx, mp, req)
		if err == nil {
			s.usage.RecordCompletion(app.Name, mp, completion.Usage, time.Since(start), attempts > 1)

			return ChatResult{
				Model:        mp.Model, // what actually served — never an echo (decision #5)
				Provider:     mp.Provider,
				Substituted:  mp.Model != req.Model,
				Message:      completion.Message,
				FinishReason: completion.FinishReason,
				Usage:        completion.Usage,
			}, nil
		}

		if errors.Is(callerCtx.Err(), context.Canceled) {
			// Caller gone: aborting stopped future token spend, but the
			// provider may bill what it already generated (decision #3).
			// There is no one to answer — the correlation ID in the edge's
			// log is the only trace.
			s.usage.RecordClientDisconnect(app.Name, mp, unobservedTokens(err, req))

			return ChatResult{}, fmt.Errorf("caller disconnected: %w", callerCtx.Err())
		}

		if ctx.Err() != nil {
			s.usage.RecordRejection(app.Name, CodeGatewayTimeout)

			return ChatResult{}, &Error{Code: CodeGatewayTimeout, Message: "total request deadline exhausted"}
		}

		if billsDespiteFailure(err) {
			s.usage.RecordDoubleSpendRisk(app.Name, mp, unobservedTokens(err, req))
		}
		// Provider-scoped failure with budget left: the next eligible model
		// provider, if any, is the one failover this request gets.
	}

	if attempts < maxAttempts && constrained {
		// The plan ran out because the app's policy refused the model
		// providers that remain — its recorded fidelity-over-availability
		// trade, not a gateway failure (decision #5).
		s.usage.RecordRejection(app.Name, CodeModelUnavailable)

		return ChatResult{}, modelUnavailable(req.Model)
	}

	s.usage.RecordRejection(app.Name, CodeUpstreamFailed)

	return ChatResult{}, &Error{Code: CodeUpstreamFailed, Message: "all eligible provider attempts failed"}
}

func (s *Service) tryModelProvider(ctx context.Context, mp ModelProvider, req ChatRequest) (Completion, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.PerTryDeadline)
	defer cancel()

	return s.provider.Complete(ctx, mp, req)
}

func modelUnavailable(model string) *Error {
	return &Error{
		Code:           CodeModelUnavailable,
		Message:        fmt.Sprintf("no eligible model provider for %s under the app's failover policy", model),
		RequestedModel: model,
	}
}
