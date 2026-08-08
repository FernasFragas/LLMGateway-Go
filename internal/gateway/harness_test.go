package gateway

// Test harness: scenario builders and fakes. Builders hide mechanics, never
// meaning — every rule under test stays visible at its call site.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The vocabulary of every scenario: the model-providers table and apps from
// the architecture brief.
var (
	gptOpenAI = ModelProvider{Model: "gpt-4.1", Provider: "openai", Endpoint: "https://api.openai.com/v1"}
	gptAzure  = ModelProvider{Model: "gpt-4.1", Provider: "azure", Endpoint: "https://azure.example/v1"}
	claude    = ModelProvider{Model: "claude-sonnet-4", Provider: "anthropic", Endpoint: "https://api.anthropic.com/v1"}
	llama     = ModelProvider{Model: "llama3", Provider: "ollama", Endpoint: "http://ollama:11434"}
)

func sameModel() FailoverPolicy { return FailoverPolicy{Kind: PolicySameModel} }
func anyModel() FailoverPolicy  { return FailoverPolicy{Kind: PolicyAny} }
func allowing(models ...string) FailoverPolicy {
	return FailoverPolicy{Kind: PolicyAllowlist, Allowlist: models}
}

// request is a valid chat request for model; max_tokens 512 is the cost cap
// the unobserved-spend assertions refer back to.
func request(model string) ChatRequest {
	return ChatRequest{
		Model:     model,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: 512,
	}
}

type fixture struct {
	t        *testing.T
	cfg      Config
	apps     fakeApps
	limiter  *fakeLimiter
	tokens   *fakeTokens
	slots    *fakeSlots
	provider *scriptedProvider
	rec      *eventLog

	svc       *Service
	callerCtx context.Context
	endCaller context.CancelFunc
}

type option func(*fixture)

// newGateway assembles a service around the brief's three model providers
// and three apps — rag-api (any), agent-service (same-model), support-bot
// (allowlist: claude-sonnet-4). Every provider serves successfully unless an
// option says otherwise.
func newGateway(t *testing.T, opts ...option) *fixture {
	t.Helper()
	f := &fixture{
		t: t,
		cfg: Config{
			ModelProviders: []ModelProvider{gptOpenAI, claude, llama},
			FailoverOrder:  []string{"openai", "anthropic", "ollama"},
			TotalDeadline:  5 * time.Second,
			PerTryDeadline: time.Second,
		},
		apps: fakeApps{
			"rag-key":   {Name: "rag-api", Policy: anyModel()},
			"agent-key": {Name: "agent-service", Policy: sameModel()},
			"bot-key":   {Name: "support-bot", Policy: allowing("claude-sonnet-4")},
		},
		limiter:  &fakeLimiter{decision: RateDecision{Allowed: true}},
		tokens:   &fakeTokens{decision: RateDecision{Allowed: true}},
		slots:    &fakeSlots{},
		provider: &scriptedProvider{stubs: map[string]stub{}},
		rec:      &eventLog{},
	}
	f.callerCtx, f.endCaller = context.WithCancel(context.Background())
	t.Cleanup(f.endCaller)
	for _, opt := range opts {
		opt(f)
	}
	svc, err := New(f.cfg, Deps{
		Apps:        f.apps,
		RateLimiter: f.limiter,
		Tokens:      f.tokens,
		Slots:       f.slots,
		Provider:    f.provider,
		Usage:       f.rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	return f
}

// chat sends a valid request as rag-api (policy: any).
func (f *fixture) chat(model string) (ChatResult, error) { return f.chatAs("rag-key", model) }

func (f *fixture) chatAs(key, model string) (ChatResult, error) { return f.send(key, request(model)) }

func (f *fixture) send(key string, req ChatRequest) (ChatResult, error) {
	f.t.Helper()
	return f.svc.Chat(f.callerCtx, key, req)
}

// --- scenario options ---

func modelProviders(providers ...ModelProvider) option {
	return func(f *fixture) { f.cfg.ModelProviders = providers }
}

func deadlines(total, perTry time.Duration) option {
	return func(f *fixture) { f.cfg.TotalDeadline, f.cfg.PerTryDeadline = total, perTry }
}

func quotaDenied(retryAfter time.Duration, quota QuotaDetail) option {
	return func(f *fixture) {
		f.limiter.decision = RateDecision{Allowed: false, RetryAfter: retryAfter, Quota: &quota}
	}
}

func limiterDown() option {
	return func(f *fixture) { f.limiter.err = errors.New("redis: connection refused") }
}

func tokenBudgetSpent(retryAfter time.Duration, quota QuotaDetail) option {
	return func(f *fixture) {
		f.tokens.decision = RateDecision{Allowed: false, RetryAfter: retryAfter, Quota: &quota}
	}
}

func tokenLimiterDown() option {
	return func(f *fixture) { f.tokens.checkErr = errors.New("redis: connection refused") }
}

func debitsFailing() option {
	return func(f *fixture) { f.tokens.settleErr = errors.New("redis: connection refused") }
}

func slotsFull(ceiling int) option {
	return func(f *fixture) { f.slots.full, f.slots.ceiling = true, ceiling }
}

func failing(provider string, kind FaultKind) option {
	return faultedWith(provider, &ProviderFault{Kind: kind, Cause: errors.New("stubbed upstream failure")})
}

func faultedWith(provider string, fault *ProviderFault) option {
	return providerDoes(provider, func(context.Context) (Completion, error) {
		return Completion{}, fault
	})
}

// hanging never answers; the attempt ends only when its deadline fires.
func hanging(provider string) option {
	return providerDoes(provider, func(ctx context.Context) (Completion, error) {
		<-ctx.Done()
		return Completion{}, ctx.Err()
	})
}

// disconnectingDuring drops the caller while provider is mid-request.
func disconnectingDuring(provider string) option {
	return func(f *fixture) {
		providerDoes(provider, func(ctx context.Context) (Completion, error) {
			f.endCaller()
			<-ctx.Done()
			return Completion{}, ctx.Err()
		})(f)
	}
}

func providerDoes(provider string, s stub) option {
	return func(f *fixture) { f.provider.stubs[provider] = s }
}

// --- fakes ---

type fakeApps map[string]App

func (m fakeApps) AppForKey(_ context.Context, key string) (App, bool) {
	app, ok := m[key]
	return app, ok
}

type fakeLimiter struct {
	decision RateDecision
	err      error
}

func (l *fakeLimiter) Allow(context.Context, string) (RateDecision, error) {
	return l.decision, l.err
}

// fakeTokens answers the budget check from a fixed decision and records every
// debit, so a scenario can assert both what was charged and what never was.
type fakeTokens struct {
	decision  RateDecision
	checkErr  error
	settleErr error
	debits    []int
}

func (l *fakeTokens) Check(context.Context, string) (RateDecision, error) {
	return l.decision, l.checkErr
}

func (l *fakeTokens) Settle(_ context.Context, _ string, tokens int) error {
	l.debits = append(l.debits, tokens)
	return l.settleErr
}

type fakeSlots struct {
	full     bool
	ceiling  int
	released int
}

func (s *fakeSlots) TryAcquire(string) (release func(), ceiling int, ok bool) {
	if s.full {
		return nil, s.ceiling, false
	}
	return func() { s.released++ }, 0, true
}

type stub func(ctx context.Context) (Completion, error)

// scriptedProvider serves every model provider successfully unless a stub
// says otherwise, and records the providers attempted, in order.
type scriptedProvider struct {
	stubs map[string]stub
	calls []string
}

func (p *scriptedProvider) Complete(ctx context.Context, mp ModelProvider, _ ChatRequest) (Completion, error) {
	p.calls = append(p.calls, mp.Provider)
	if s, ok := p.stubs[mp.Provider]; ok {
		return s(ctx)
	}

	return Completion{
		Message:      Message{Role: RoleAssistant, Content: "ok"},
		FinishReason: FinishStop,
		Usage:        Usage{PromptTokens: 21, CompletionTokens: 30, TotalTokens: 51},
	}, nil
}

type spend struct {
	provider string
	tokens   int
}

// eventLog records what the service reported to observability.
type eventLog struct {
	completions  int
	failedOver   bool
	rejections   []ErrorCode
	failOpens    int
	doubleSpends []spend
	disconnects  []spend
}

func (e *eventLog) RecordCompletion(_ string, _ ModelProvider, _ Usage, _ time.Duration, failedOver bool) {
	e.completions++
	e.failedOver = failedOver
}

func (e *eventLog) RecordRejection(_ string, code ErrorCode) {
	e.rejections = append(e.rejections, code)
}

func (e *eventLog) RecordRateLimiterFailOpen(string) { e.failOpens++ }

func (e *eventLog) RecordDoubleSpendRisk(_ string, r ModelProvider, tokens int) {
	e.doubleSpends = append(e.doubleSpends, spend{r.Provider, tokens})
}

func (e *eventLog) RecordClientDisconnect(_ string, r ModelProvider, tokens int) {
	e.disconnects = append(e.disconnects, spend{r.Provider, tokens})
}

// --- assertions ---

// wantErrorCode asserts err is the gateway's error contract with the given code.
func wantErrorCode(t *testing.T, err error, code ErrorCode) *Error {
	t.Helper()

	var gwErr *Error
	if !errors.As(err, &gwErr) {
		t.Fatalf("error = %v, want *gateway.Error", err)
	}
	if gwErr.Code != code {
		t.Fatalf("error code = %q, want %q", gwErr.Code, code)
	}

	return gwErr
}
