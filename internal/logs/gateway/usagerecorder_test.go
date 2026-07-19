package logs

import (
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestRecorderAttributesEveryFact(t *testing.T) {
	usage := gateway.Usage{PromptTokens: 21, CompletionTokens: 30, TotalTokens: 51}
	tests := []struct {
		name string
		emit func(r *UsageRecorder)
		want []string
	}{
		{
			name: "a completion carries the model provider, tokens, and the failover flag",
			emit: func(r *UsageRecorder) {
				r.RecordCompletion("rag-api", gptOpenAI, usage, 250*time.Millisecond, true)
			},
			want: []string{"rag-api", "openai", "gpt-4.1", "completion_tokens=30", "failed_over=true"},
		},
		{
			name: "a rejection carries the outcome code",
			emit: func(r *UsageRecorder) { r.RecordRejection("rag-api", gateway.CodeQuotaExceeded) },
			want: []string{"rag-api", "quota_exceeded"},
		},
		{
			name: "a fail-open names the app running unmetered",
			emit: func(r *UsageRecorder) { r.RecordRateLimiterFailOpen("rag-api") },
			want: []string{"rag-api", "unmetered"},
		},
		{
			name: "a double-spend risk carries the bounded estimate",
			emit: func(r *UsageRecorder) { r.RecordDoubleSpendRisk("rag-api", gptOpenAI, 512) },
			want: []string{"rag-api", "openai", "estimated_tokens=512"},
		},
		{
			name: "a client disconnect carries the bounded estimate",
			emit: func(r *UsageRecorder) { r.RecordClientDisconnect("rag-api", gptOpenAI, 512) },
			want: []string{"rag-api", "openai", "estimated_tokens=512"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, out := captured(t)
			tt.emit(NewUsageRecorder(log))
			wantLogged(t, out, tt.want...)
		})
	}
}
