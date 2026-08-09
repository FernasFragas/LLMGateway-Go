package metrics

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// UsageRecorder counts the facts the core reports and forwards every call to
// next unchanged — typically the logging recorder, so one call both meters
// and narrates. Token totals are the only sums; app, model provider, and
// error code aren't bounded enough to carry as counter dimensions, so
// RejectionsByCode is the one breakdown kept, matching the fixed ErrorCode
// set.
type UsageRecorder struct {
	next gateway.UsageRecorder

	completions      atomic.Int64
	failedOvers      atomic.Int64
	promptTokens     atomic.Int64
	completionTokens atomic.Int64

	rejections atomic.Int64

	rateLimiterFailOpens atomic.Int64

	// Each unobservable-spend event is counted twice over: how often it
	// happened, and the upper-bound tokens it may have cost. The pair is the
	// point — the count alone says an incident occurred, and only the token
	// sum says whether it mattered against a real bill (decision #6).
	doubleSpendRisks       atomic.Int64
	doubleSpendTokens      atomic.Int64
	clientDisconnects      atomic.Int64
	clientDisconnectTokens atomic.Int64

	mu               sync.Mutex
	rejectionsByCode map[gateway.ErrorCode]int64
}

// NewUsageRecorder wraps next, counting every fact before forwarding it.
func NewUsageRecorder(next gateway.UsageRecorder) *UsageRecorder {
	return &UsageRecorder{next: next, rejectionsByCode: make(map[gateway.ErrorCode]int64)}
}

func (r *UsageRecorder) RecordCompletion(app string, mp gateway.ModelProvider, usage gateway.Usage, latency time.Duration, failedOver bool) {
	r.completions.Add(1)
	r.promptTokens.Add(int64(usage.PromptTokens))
	r.completionTokens.Add(int64(usage.CompletionTokens))
	if failedOver {
		r.failedOvers.Add(1)
	}

	r.next.RecordCompletion(app, mp, usage, latency, failedOver)
}

func (r *UsageRecorder) RecordRejection(app string, code gateway.ErrorCode) {
	r.rejections.Add(1)

	r.mu.Lock()
	r.rejectionsByCode[code]++
	r.mu.Unlock()

	r.next.RecordRejection(app, code)
}

func (r *UsageRecorder) RecordRateLimiterFailOpen(app string) {
	r.rateLimiterFailOpens.Add(1)

	r.next.RecordRateLimiterFailOpen(app)
}

func (r *UsageRecorder) RecordDoubleSpendRisk(app string, mp gateway.ModelProvider, estimatedTokens int) {
	r.doubleSpendRisks.Add(1)
	addEstimate(&r.doubleSpendTokens, estimatedTokens)

	r.next.RecordDoubleSpendRisk(app, mp, estimatedTokens)
}

func (r *UsageRecorder) RecordClientDisconnect(app string, mp gateway.ModelProvider, estimatedTokens int) {
	r.clientDisconnects.Add(1)
	addEstimate(&r.clientDisconnectTokens, estimatedTokens)

	r.next.RecordClientDisconnect(app, mp, estimatedTokens)
}

// addEstimate accumulates an upper-bound token estimate, ignoring anything
// that isn't a positive number. These feed a monotonic counter, and a
// negative would run it backwards — an exporter's cardinal sin, and one no
// caller could see from the metric afterwards.
func addEstimate(total *atomic.Int64, tokens int) {
	if tokens > 0 {
		total.Add(int64(tokens))
	}
}

// Completions reports how many requests this instance recorded as served.
func (r *UsageRecorder) Completions() int64 { return r.completions.Load() }

// FailedOvers reports how many served completions were the result of a
// failover.
func (r *UsageRecorder) FailedOvers() int64 { return r.failedOvers.Load() }

// PromptTokens reports the total prompt tokens billed across every served
// completion.
func (r *UsageRecorder) PromptTokens() int64 { return r.promptTokens.Load() }

// CompletionTokens reports the total completion tokens billed across every
// served completion.
func (r *UsageRecorder) CompletionTokens() int64 { return r.completionTokens.Load() }

// Rejections reports how many requests this instance recorded as refused.
func (r *UsageRecorder) Rejections() int64 { return r.rejections.Load() }

// RejectionsByCode reports how many rejections carried code.
func (r *UsageRecorder) RejectionsByCode(code gateway.ErrorCode) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.rejectionsByCode[code]
}

// RateLimiterFailOpens reports how many requests were admitted unmetered
// because the rate limiter itself failed.
func (r *UsageRecorder) RateLimiterFailOpens() int64 { return r.rateLimiterFailOpens.Load() }

// DoubleSpendRisks reports how many failovers risked double-billing an
// abandoned attempt.
func (r *UsageRecorder) DoubleSpendRisks() int64 { return r.doubleSpendRisks.Load() }

// DoubleSpendTokens reports the upper-bound tokens those failovers may have
// been billed for: each attempt's parsed usage when the fault carried any,
// otherwise the request's own max_tokens.
func (r *UsageRecorder) DoubleSpendTokens() int64 { return r.doubleSpendTokens.Load() }

// ClientDisconnects reports how many requests a caller abandoned mid-flight.
func (r *UsageRecorder) ClientDisconnects() int64 { return r.clientDisconnects.Load() }

// ClientDisconnectTokens reports the upper-bound tokens those abandoned
// requests may still have been billed for.
func (r *UsageRecorder) ClientDisconnectTokens() int64 { return r.clientDisconnectTokens.Load() }
