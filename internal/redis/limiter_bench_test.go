package redis

// Benchmarks against a real Redis, for the one question ADR-003 left owed:
// what the token currency's two extra round trips cost per served request,
// measured against the brief's < 50 ms p95 overhead budget.
//
// They need a real server because the fake in harness_test.go answers from a
// map on localhost — it measures this package's framing, not Redis. Skipped
// unless an address is given:
//
//	docker run -d --rm -p 6399:6379 redis:7-alpine
//	REDIS_ADDR=127.0.0.1:6399 go test ./internal/redis -bench . -benchtime 2000x -run '^$'

import (
	"context"
	"os"
	"testing"
)

func benchClient(b *testing.B) *Client {
	b.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		b.Skip("set REDIS_ADDR to benchmark against a real Redis")
	}
	client, err := NewClient(addr, redisBenchPool)
	if err != nil {
		b.Fatalf("NewClient: %v", err)
	}
	b.Cleanup(func() { _ = client.Close() })

	return client
}

// redisBenchPool matches main's redisPoolSize, so the pooling behavior under
// measurement is the one that ships.
const redisBenchPool = 8

// BenchmarkRequestRateAllow is the currency that already shipped — the
// baseline the token currency's cost is read against.
func BenchmarkRequestRateAllow(b *testing.B) {
	l := NewLimiter(benchClient(b), map[string]int{"rag-api": 1 << 30})
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		if _, err := l.Allow(ctx, "rag-api"); err != nil {
			b.Fatalf("Allow: %v", err)
		}
	}
}

func BenchmarkTokenBudgetCheck(b *testing.B) {
	l := NewTokenLimiter(benchClient(b), map[string]int{"rag-api": 1 << 30})
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		if _, err := l.Check(ctx, "rag-api"); err != nil {
			b.Fatalf("Check: %v", err)
		}
	}
}

func BenchmarkTokenBudgetSettle(b *testing.B) {
	l := NewTokenLimiter(benchClient(b), map[string]int{"rag-api": 1 << 30})
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		if err := l.Settle(ctx, "rag-api", 51); err != nil {
			b.Fatalf("Settle: %v", err)
		}
	}
}

// BenchmarkServedRequestLimiters is what one served request now pays in the
// limiters: the rps increment, the budget read, and the debit.
func BenchmarkServedRequestLimiters(b *testing.B) {
	client := benchClient(b)
	rate := NewLimiter(client, map[string]int{"rag-api": 1 << 30})
	tokens := NewTokenLimiter(client, map[string]int{"rag-api": 1 << 30})
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		if _, err := rate.Allow(ctx, "rag-api"); err != nil {
			b.Fatalf("Allow: %v", err)
		}
		if _, err := tokens.Check(ctx, "rag-api"); err != nil {
			b.Fatalf("Check: %v", err)
		}
		if err := tokens.Settle(ctx, "rag-api", 51); err != nil {
			b.Fatalf("Settle: %v", err)
		}
	}
}

// BenchmarkUnmeteredAppCostsNothing pins the zero-budget path: an app that
// configures no budget must not pay a round trip for the currency.
func BenchmarkUnmeteredAppCostsNothing(b *testing.B) {
	l := NewTokenLimiter(benchClient(b), map[string]int{"rag-api": 0})
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		if _, err := l.Check(ctx, "rag-api"); err != nil {
			b.Fatalf("Check: %v", err)
		}
		if err := l.Settle(ctx, "rag-api", 51); err != nil {
			b.Fatalf("Settle: %v", err)
		}
	}
}
