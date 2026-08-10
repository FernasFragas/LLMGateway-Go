package metrics

import (
	"slices"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// UsageRecorder counts the facts the core reports and forwards every call to
// next unchanged — typically the logging recorder, so one call both meters
// and narrates.
//
// Every counter carries the calling app, because attributing spend to the
// app that caused it is the design's own promise and cannot be recovered
// from a process-wide total afterwards. The app set comes from config at
// construction and never grows (see doc.go), so the dimension costs one map
// read per record and no lock at all.
type UsageRecorder struct {
	next gateway.UsageRecorder

	apps []string

	completions      *keyedCounter[string]
	failedOvers      *keyedCounter[string]
	promptTokens     *keyedCounter[string]
	completionTokens *keyedCounter[string]

	rejections *keyedCounter[appCode]

	rateLimiterFailOpens *keyedCounter[string]

	// Each unobservable-spend event is counted twice over: how often it
	// happened, and the upper-bound tokens it may have cost. The count alone
	// says an incident occurred; only the token sum says whether it mattered
	// against a real bill (decision #6).
	doubleSpendRisks       *keyedCounter[string]
	doubleSpendTokens      *keyedCounter[string]
	clientDisconnects      *keyedCounter[string]
	clientDisconnectTokens *keyedCounter[string]
}

// NewUsageRecorder wraps next, counting every fact before forwarding it.
// apps is the closed set of configured app names — the only values any
// counter here will attribute to, and the bound on how many series this
// instance can ever produce.
func NewUsageRecorder(next gateway.UsageRecorder, apps []string) *UsageRecorder {
	own := slices.Clone(apps)
	slices.Sort(own)

	return &UsageRecorder{
		next: next,
		apps: own,

		completions:      newKeyedCounter(own),
		failedOvers:      newKeyedCounter(own),
		promptTokens:     newKeyedCounter(own),
		completionTokens: newKeyedCounter(own),

		rejections: newKeyedCounter(appCodeKeys(own)),

		rateLimiterFailOpens: newKeyedCounter(own),

		doubleSpendRisks:       newKeyedCounter(own),
		doubleSpendTokens:      newKeyedCounter(own),
		clientDisconnects:      newKeyedCounter(own),
		clientDisconnectTokens: newKeyedCounter(own),
	}
}

// Apps reports the configured app names, sorted. The exporter enumerates
// this rather than keeping its own list, so the two cannot drift.
func (r *UsageRecorder) Apps() []string { return slices.Clone(r.apps) }

func (r *UsageRecorder) RecordCompletion(app string, mp gateway.ModelProvider, usage gateway.Usage, latency time.Duration, failedOver bool) {
	r.completions.add(app, 1)
	r.promptTokens.add(app, int64(usage.PromptTokens))
	r.completionTokens.add(app, int64(usage.CompletionTokens))
	if failedOver {
		r.failedOvers.add(app, 1)
	}

	r.next.RecordCompletion(app, mp, usage, latency, failedOver)
}

func (r *UsageRecorder) RecordRejection(app string, code gateway.ErrorCode) {
	r.rejections.add(appCode{app: app, code: code}, 1)

	r.next.RecordRejection(app, code)
}

func (r *UsageRecorder) RecordRateLimiterFailOpen(app string) {
	r.rateLimiterFailOpens.add(app, 1)

	r.next.RecordRateLimiterFailOpen(app)
}

func (r *UsageRecorder) RecordDoubleSpendRisk(app string, mp gateway.ModelProvider, estimatedTokens int) {
	r.doubleSpendRisks.add(app, 1)
	addEstimate(r.doubleSpendTokens, app, estimatedTokens)

	r.next.RecordDoubleSpendRisk(app, mp, estimatedTokens)
}

func (r *UsageRecorder) RecordClientDisconnect(app string, mp gateway.ModelProvider, estimatedTokens int) {
	r.clientDisconnects.add(app, 1)
	addEstimate(r.clientDisconnectTokens, app, estimatedTokens)

	r.next.RecordClientDisconnect(app, mp, estimatedTokens)
}

// addEstimate accumulates an upper-bound token estimate, ignoring anything
// that isn't a positive number. These feed monotonic counters, and a
// negative would run one backwards — an exporter's cardinal sin, and one no
// caller could see from the metric afterwards.
func addEstimate(total *keyedCounter[string], app string, tokens int) {
	if tokens > 0 {
		total.add(app, int64(tokens))
	}
}

// Completions reports how many requests this instance recorded as served.
func (r *UsageRecorder) Completions() int64 { return r.completions.total() }

// CompletionsByApp reports how many app was served.
func (r *UsageRecorder) CompletionsByApp(app string) int64 { return r.completions.get(app) }

// FailedOvers reports how many served completions were the result of a
// failover.
func (r *UsageRecorder) FailedOvers() int64 { return r.failedOvers.total() }

// FailedOversByApp reports how many of app's completions needed a failover.
func (r *UsageRecorder) FailedOversByApp(app string) int64 { return r.failedOvers.get(app) }

// PromptTokens reports the total prompt tokens billed across every served
// completion.
func (r *UsageRecorder) PromptTokens() int64 { return r.promptTokens.total() }

// PromptTokensByApp reports app's own prompt tokens — one half of what
// reconciling a provider bill against a single caller requires.
func (r *UsageRecorder) PromptTokensByApp(app string) int64 { return r.promptTokens.get(app) }

// CompletionTokens reports the total completion tokens billed across every
// served completion.
func (r *UsageRecorder) CompletionTokens() int64 { return r.completionTokens.total() }

// CompletionTokensByApp reports app's own completion tokens. Providers price
// these separately from prompt tokens, which is why the two never merge.
func (r *UsageRecorder) CompletionTokensByApp(app string) int64 {
	return r.completionTokens.get(app)
}

// Rejections reports how many requests this instance recorded as refused.
func (r *UsageRecorder) Rejections() int64 { return r.rejections.total() }

// RejectionsBy reports how many of app's requests were refused with code.
func (r *UsageRecorder) RejectionsBy(app string, code gateway.ErrorCode) int64 {
	return r.rejections.get(appCode{app: app, code: code})
}

// RateLimiterFailOpens reports how many requests were admitted unmetered
// because a limiter itself failed.
func (r *UsageRecorder) RateLimiterFailOpens() int64 { return r.rateLimiterFailOpens.total() }

// RateLimiterFailOpensByApp reports how many of app's requests went
// unmetered — the app whose limits stopped applying, which is the one an
// operator needs named.
func (r *UsageRecorder) RateLimiterFailOpensByApp(app string) int64 {
	return r.rateLimiterFailOpens.get(app)
}

// DoubleSpendRisks reports how many failovers risked double-billing an
// abandoned attempt.
func (r *UsageRecorder) DoubleSpendRisks() int64 { return r.doubleSpendRisks.total() }

// DoubleSpendRisksByApp reports how many of them were app's.
func (r *UsageRecorder) DoubleSpendRisksByApp(app string) int64 {
	return r.doubleSpendRisks.get(app)
}

// DoubleSpendTokens reports the upper-bound tokens those failovers may have
// been billed for: each attempt's parsed usage when the fault carried any,
// otherwise the request's own max_tokens.
func (r *UsageRecorder) DoubleSpendTokens() int64 { return r.doubleSpendTokens.total() }

// DoubleSpendTokensByApp reports app's share of that estimate.
func (r *UsageRecorder) DoubleSpendTokensByApp(app string) int64 {
	return r.doubleSpendTokens.get(app)
}

// ClientDisconnects reports how many requests a caller abandoned mid-flight.
func (r *UsageRecorder) ClientDisconnects() int64 { return r.clientDisconnects.total() }

// ClientDisconnectsByApp reports how many of them were app's.
func (r *UsageRecorder) ClientDisconnectsByApp(app string) int64 {
	return r.clientDisconnects.get(app)
}

// ClientDisconnectTokens reports the upper-bound tokens those abandoned
// requests may still have been billed for.
func (r *UsageRecorder) ClientDisconnectTokens() int64 { return r.clientDisconnectTokens.total() }

// ClientDisconnectTokensByApp reports app's share of that estimate.
func (r *UsageRecorder) ClientDisconnectTokensByApp(app string) int64 {
	return r.clientDisconnectTokens.get(app)
}
