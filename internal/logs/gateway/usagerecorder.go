package logs

import (
	"log/slog"

	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// UsageRecorder turns the core's usage facts into structured log lines.
// slog never blocks and never fails the caller, honoring the port's
// contract: observability never gates the data path.
type UsageRecorder struct {
	log *slog.Logger
}

// NewUsageRecorder returns a recorder writing to log; nil means slog.Default().
func NewUsageRecorder(log *slog.Logger) *UsageRecorder {
	return &UsageRecorder{log: logs.OrDefault(log)}
}

func (r *UsageRecorder) RecordCompletion(app string, mp gateway.ModelProvider, usage gateway.Usage, latency time.Duration, failedOver bool) {
	r.log.Info("completion served",
		"app", app,
		"provider", mp.Provider,
		"model", mp.Model,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"latency", latency,
		"failed_over", failedOver,
	)
}

func (r *UsageRecorder) RecordRejection(app string, code gateway.ErrorCode) {
	r.log.Info("request rejected", "app", app, "code", string(code))
}

func (r *UsageRecorder) RecordRateLimiterFailOpen(app string) {
	r.log.Warn("request admitted unmetered", "app", app)
}

func (r *UsageRecorder) RecordDoubleSpendRisk(app string, mp gateway.ModelProvider, estimatedTokens int) {
	r.log.Warn("failover risks double spend",
		"app", app, "provider", mp.Provider, "model", mp.Model,
		"estimated_tokens", estimatedTokens,
	)
}

func (r *UsageRecorder) RecordClientDisconnect(app string, mp gateway.ModelProvider, estimatedTokens int) {
	r.log.Warn("caller disconnected mid-request",
		"app", app, "provider", mp.Provider, "model", mp.Model,
		"estimated_tokens", estimatedTokens,
	)
}
