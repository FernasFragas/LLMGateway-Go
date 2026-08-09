//go:build integration

package redis

// The fake in harness_test.go answers EVAL by grepping the script text for
// INCRBY — it never executes Lua, never expires a key, and never frames a
// reply the way a server does. Everything the scripts exist to guarantee is
// therefore unproven by the default suite: that INCR and EXPIRE are atomic,
// that a TTL is set once and not renewed, that a never-written budget reads
// as 0 rather than erroring, and that this hand-rolled RESP client agrees
// with a real server about error replies and pooled reuse.
//
// This file closes that gap against redis:7-alpine in a container. It is
// build-tagged because `go test ./...` must stay Docker-free:
//
//	make test-integration
//	go test -tags=integration -count=1 ./internal/redis
//
// Every test names its own app, so they share one container without
// colliding, and pins `now` to a fixed instant so its key is a literal the
// assertions can read directly — if the production key formula ever changes,
// these fail rather than quietly testing nothing.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// redisImage is the version deploy/dev/redis.yaml schedules, so the suite
// proves the scripts against the server the cluster actually runs.
const redisImage = "redis:7-alpine"

// storeAddr is the container's host:port, set once by TestMain.
var storeAddr string

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := tcredis.Run(ctx, redisImage)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start %s: %v\n(is Docker running? this suite needs it)\n", redisImage, err)
		os.Exit(1)
	}

	if err := resolveAddr(ctx, container); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		_ = testcontainers.TerminateContainer(container)
		os.Exit(1)
	}

	// The benchmarks reuse this container under the tag; untagged they keep
	// their own REDIS_ADDR skip.
	integrationAddr = storeAddr

	code := m.Run()

	_ = testcontainers.TerminateContainer(container)
	os.Exit(code)
}

// resolveAddr turns the module's redis:// URI into the host:port NewClient
// takes.
func resolveAddr(ctx context.Context, container *tcredis.RedisContainer) error {
	uri, err := container.ConnectionString(ctx)
	if err != nil {
		return fmt.Errorf("connection string: %w", err)
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		return fmt.Errorf("parse connection string %q: %w", uri, err)
	}
	storeAddr = parsed.Host

	return nil
}

// ─── scenario builders ──────────────────────────────────────────────────────

func realClient(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient(storeAddr, 2)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// appAt names an app unique to this test and pins the limiter's clock, so
// every call in one test lands on one key and that key belongs to no other
// test in the shared container.
func appAt(t *testing.T, at time.Time) (string, func() time.Time) {
	t.Helper()

	return t.Name(), func() time.Time { return at }
}

// ttlOf reads a key's remaining lifetime in seconds. Redis answers TTL with
// an integer, which is exactly the reply shape this client reads: -2 for a
// key that is gone, -1 for one with no expiry set.
func ttlOf(t *testing.T, client *Client, key string) int64 {
	t.Helper()
	ttl, err := client.Int(context.Background(), "TTL", key)
	if err != nil {
		t.Fatalf("TTL %s: %v", key, err)
	}

	return ttl
}

// ─── the request-rate currency ──────────────────────────────────────────────

func TestTheFirstIncrementSetsTheTTLAndLaterOnesNeverRenewIt(t *testing.T) {
	// A renewed TTL is the bug that never shows up against the fake: every
	// request would push the expiry out, so a busy app's key would outlive
	// its window indefinitely and its count would never reset.
	client := realClient(t)
	app, at := appAt(t, time.Unix(1000, 0))
	l := NewLimiter(client, map[string]int{app: 100})
	l.now = at

	key := "llmgw:rps:" + app + ":1000" // window index = 1000s / 1s

	if _, err := l.Allow(context.Background(), app); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	first := ttlOf(t, client, key)
	if first <= 0 {
		t.Fatalf("TTL after the first increment = %d, want a positive lifetime — a key with no expiry refuses its app forever", first)
	}

	time.Sleep(1100 * time.Millisecond)

	if _, err := l.Allow(context.Background(), app); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	if second := ttlOf(t, client, key); second >= first {
		t.Errorf("TTL went %d → %d across a second increment, want it to keep counting down — EXPIRE must run only when INCR returns 1", first, second)
	}
}

func TestACounterReallyExpiresRatherThanOnlyChangingKeys(t *testing.T) {
	// The fake-backed test moves the clock, which proves the key *name*
	// rolls over. This holds the clock still and lets the server expire the
	// key underneath it — the property the TTL exists for, and the one no
	// map-backed fake can have.
	client := realClient(t)
	app, at := appAt(t, time.Unix(2000, 0))
	l := NewLimiter(client, map[string]int{app: 1})
	l.now = at

	if d, err := l.Allow(context.Background(), app); err != nil || !d.Allowed {
		t.Fatalf("first request refused: %+v %v", d, err)
	}
	if d, _ := l.Allow(context.Background(), app); d.Allowed {
		t.Fatal("the second request in one window should be refused")
	}

	// The limiter asks for window + 1s, so the key is gone by now even
	// though the clock — and therefore the key name — has not moved.
	time.Sleep(window + 1500*time.Millisecond)

	if d, err := l.Allow(context.Background(), app); err != nil || !d.Allowed {
		t.Errorf("the count did not restart after the key expired: %+v %v — the app would stay refused until its clock rolled the key over", d, err)
	}
}

// ─── the token currency ─────────────────────────────────────────────────────

func TestACheckReadsExactlyWhatADebitWrote(t *testing.T) {
	// End to end through the server: INCRBY's integer reply, the key's own
	// framing, and spentScript's tonumber conversion have to agree, or the
	// budget an operator configured is not the budget being enforced.
	client := realClient(t)
	app, at := appAt(t, time.Unix(600, 0))
	l := NewTokenLimiter(client, map[string]int{app: 100})
	l.now = at

	if err := l.Settle(context.Background(), app, 120); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	d, err := l.Check(context.Background(), app)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Fatal("an app 120 tokens into a 100-token budget was admitted")
	}
	if d.Quota == nil || d.Quota.Used != 120 || d.Quota.Limit != 100 {
		t.Errorf("Quota = %+v, want the debit read back exactly (used 120 of 100)", d.Quota)
	}
	if d.Quota != nil && d.Quota.WindowSeconds != 60 {
		t.Errorf("WindowSeconds = %d, want 60 — the field that tells a token refusal from an rps one", d.Quota.WindowSeconds)
	}
}

func TestABudgetNeverWrittenReadsAsZeroRatherThanFailing(t *testing.T) {
	// GET answers nil for a key that was never written, and tonumber(nil)
	// raises a Lua error the client would surface as a store failure — the
	// core would then fail open and leave the app unmetered for its whole
	// first window. spentScript's `if not v` branch is what prevents it, and
	// only a real server can exercise it.
	client := realClient(t)
	app, at := appAt(t, time.Unix(660, 0))
	l := NewTokenLimiter(client, map[string]int{app: 100})
	l.now = at

	d, err := l.Check(context.Background(), app)
	if err != nil {
		t.Fatalf("Check on an untouched budget failed: %v — a fresh window must read as zero spend, not as a store error", err)
	}
	if !d.Allowed {
		t.Error("an app that has spent nothing was refused")
	}
}

func TestTheDebitTTLIsSetOnceForTheWindow(t *testing.T) {
	// debitScript's own version of the rps rule: the first write is the one
	// whose result equals its increment, and only that one sets the expiry.
	client := realClient(t)
	app, at := appAt(t, time.Unix(720, 0))
	l := NewTokenLimiter(client, map[string]int{app: 1000})
	l.now = at

	key := "llmgw:tpm:" + app + ":12" // window index = 720s / 60s

	if err := l.Settle(context.Background(), app, 10); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	first := ttlOf(t, client, key)
	if first <= 0 {
		t.Fatalf("TTL after the first debit = %d, want a positive lifetime", first)
	}

	time.Sleep(1100 * time.Millisecond)

	if err := l.Settle(context.Background(), app, 10); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	if second := ttlOf(t, client, key); second >= first {
		t.Errorf("TTL went %d → %d across a second debit, want it to keep counting down", first, second)
	}
}

// ─── the client against a real server ───────────────────────────────────────

func TestAScriptErrorIsReportedAndLeavesThePoolUsable(t *testing.T) {
	// The fake replies with a canned error string on demand. A real server
	// decides for itself what is wrong and answers mid-conversation, which
	// is the case that can strand a connection: half a reply left in the
	// buffer would corrupt every later call on it.
	client := realClient(t)
	app, at := appAt(t, time.Unix(3000, 0))

	_, err := client.Int(context.Background(), "EVAL", "this is not lua", "1", "llmgw:rps:"+app+":3000")
	if err == nil {
		t.Fatal("a malformed script was accepted; the server's own complaint must travel back as an error")
	}
	if !strings.HasPrefix(err.Error(), "redis: ") {
		t.Errorf("error = %v, want the store's message carried through this package's prefix", err)
	}

	l := NewLimiter(client, map[string]int{app: 1})
	l.now = at

	if d, err := l.Allow(context.Background(), app); err != nil || !d.Allowed {
		t.Errorf("the next call after an error reply failed: %+v %v — an error must cost one connection, never the pool", d, err)
	}
}

func TestPooledConnectionsSurviveASequenceOfCalls(t *testing.T) {
	// Against the fake this counts accepts on a listener. Against a real
	// server it proves the stronger thing: the reply framing leaves no
	// residue, so a connection handed back to the pool is genuinely clean.
	client := realClient(t)
	app, at := appAt(t, time.Unix(4000, 0))
	l := NewLimiter(client, map[string]int{app: 1 << 20})
	l.now = at

	for i := range 25 {
		d, err := l.Allow(context.Background(), app)
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if !d.Allowed {
			t.Fatalf("call %d refused against a quota of 2^20", i+1)
		}
	}

	if used := ttlOf(t, client, "llmgw:rps:"+app+":4000"); used <= 0 {
		t.Errorf("TTL = %d after 25 calls, want the key still alive with its original lifetime", used)
	}
}
