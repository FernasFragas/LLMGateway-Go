package redis

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAnAppIsRefusedOncePastItsQuota(t *testing.T) {
	store := fakeRedis(t)
	l := limiterAt(t, store, map[string]int{"rag-api": 3})

	for i := range 3 {
		if d, err := l.Allow(context.Background(), "rag-api"); err != nil || !d.Allowed {
			t.Fatalf("request %d refused early: %+v %v", i+1, d, err)
		}
	}

	d, err := l.Allow(context.Background(), "rag-api")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if d.Allowed {
		t.Fatal("the fourth request was admitted against a quota of three")
	}
	if d.RetryAfter <= 0 || d.RetryAfter > window {
		t.Errorf("RetryAfter = %v, want the time left in the window so the caller is told when to return", d.RetryAfter)
	}
	if d.Quota == nil || d.Quota.Limit != 3 || d.Quota.Used != 4 {
		t.Errorf("Quota = %+v, want the window that refused, with the request counted", d.Quota)
	}
}

func TestOneAppsQuotaNeverTouchesAnother(t *testing.T) {
	store := fakeRedis(t)
	l := limiterAt(t, store, map[string]int{"rag-api": 1, "agent-service": 1})

	_, _ = l.Allow(context.Background(), "rag-api")
	if d, _ := l.Allow(context.Background(), "rag-api"); d.Allowed {
		t.Fatal("rag-api should be over quota")
	}

	if d, err := l.Allow(context.Background(), "agent-service"); err != nil || !d.Allowed {
		t.Errorf("agent-service refused because of rag-api's usage: %+v %v", d, err)
	}
}

func TestANewWindowStartsTheCountOver(t *testing.T) {
	store := fakeRedis(t)
	l := limiterAt(t, store, map[string]int{"rag-api": 1})
	at := time.Unix(1000, 0)
	l.now = func() time.Time { return at }

	_, _ = l.Allow(context.Background(), "rag-api")
	if d, _ := l.Allow(context.Background(), "rag-api"); d.Allowed {
		t.Fatal("the second request in one window should be refused")
	}

	at = at.Add(window)

	if d, err := l.Allow(context.Background(), "rag-api"); err != nil || !d.Allowed {
		t.Errorf("the next window did not start clean: %+v %v", d, err)
	}
}

func TestTheCounterIsSentWithATTLSoAQuotaIsNeverPermanent(t *testing.T) {
	// Without the expiry an app that hit its quota once would stay refused
	// forever — the failure INCR and EXPIRE share a script to prevent.
	store := fakeRedis(t)
	l := limiterAt(t, store, map[string]int{"rag-api": 1})

	if _, err := l.Allow(context.Background(), "rag-api"); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	sent := store.lastCommand()
	if len(sent) == 0 || !strings.Contains(sent[1], "EXPIRE") {
		t.Fatalf("script = %q, want it to set an expiry alongside the increment", sent)
	}
	if ttl := sent[len(sent)-1]; ttl == "" || ttl == "0" {
		t.Errorf("ttl argument = %q, want a positive lifetime", ttl)
	}
}

func TestAnUnreachableStoreReportsInsteadOfRefusing(t *testing.T) {
	// The core fails open on this error (decision #1). The limiter's job is
	// to say the store failed, never to decide what that means.
	client, err := NewClient("127.0.0.1:1", 2) // nothing listens here
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	l := NewLimiter(client, map[string]int{"rag-api": 1})

	d, err := l.Allow(context.Background(), "rag-api")
	if err == nil {
		t.Fatal("a dead store must be reported, so the core can fail open and record the degradation")
	}
	if d.Allowed {
		t.Error("the limiter must not decide to admit; that call belongs to the core")
	}
}

func TestZeroMeansUnmeteredAndNeverCallsTheStore(t *testing.T) {
	store := fakeRedis(t)
	l := limiterAt(t, store, map[string]int{"declared": 0})

	for range 50 {
		if d, err := l.Allow(context.Background(), "declared"); err != nil || !d.Allowed {
			t.Fatalf("a zero quota refused a request; zero means unmetered: %+v %v", d, err)
		}
	}
	if d, err := l.Allow(context.Background(), "never-configured"); err != nil || !d.Allowed {
		t.Fatalf("an app with no entry was metered: %+v %v", d, err)
	}
	if got := store.commands(); got != 0 {
		t.Errorf("%d commands sent for unmetered apps, want none — an unmetered app should not cost a round trip", got)
	}
}

func TestAStoreErrorReplyTravels(t *testing.T) {
	store := fakeRedis(t)
	store.replyErr = "LOADING Redis is loading the dataset in memory"
	l := limiterAt(t, store, map[string]int{"rag-api": 1})

	if _, err := l.Allow(context.Background(), "rag-api"); err == nil ||
		!strings.Contains(err.Error(), "LOADING") {
		t.Errorf("error = %v, want the store's own message carried through", err)
	}
}

func TestConnectionsAreReusedAcrossCalls(t *testing.T) {
	store := fakeRedis(t)
	l := limiterAt(t, store, map[string]int{"rag-api": 1000})

	for range 5 {
		if _, err := l.Allow(context.Background(), "rag-api"); err != nil {
			t.Fatalf("Allow: %v", err)
		}
	}

	if got := store.connections(); got != 1 {
		t.Errorf("opened %d connections for 5 calls, want 1 reused from the pool", got)
	}
}

// ─── a fake Redis ───────────────────────────────────────────────────────────

// redisServer speaks just enough RESP to answer the limiter: it parses the
// command array and answers EVAL with a per-key counter.
type redisServer struct {
	addr     string
	replyErr string

	mu     sync.Mutex
	counts map[string]int64
	last   []string
	cmds   int
	conns  int
}

func (s *redisServer) commands() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.cmds
}

func (s *redisServer) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.conns
}

func (s *redisServer) lastCommand() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.last
}

func fakeRedis(t *testing.T) *redisServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	s := &redisServer{addr: ln.Addr().String(), counts: map[string]int64{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns++
			s.mu.Unlock()
			go s.serve(c)
		}
	}()

	return s
}

func (s *redisServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)

	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}

		s.mu.Lock()
		s.cmds++
		s.last = args
		if s.replyErr != "" {
			s.mu.Unlock()
			_, _ = c.Write([]byte("-" + s.replyErr + "\r\n"))
			continue
		}
		// args: EVAL <script> 1 <key> <ttl>
		var n int64
		if len(args) >= 4 {
			s.counts[args[3]]++
			n = s.counts[args[3]]
		}
		s.mu.Unlock()

		_, _ = c.Write([]byte(":" + strconv.FormatInt(n, 10) + "\r\n"))
	}
}

// readCommand parses one RESP array of bulk strings.
func readCommand(r *bufio.Reader) ([]string, error) {
	header, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(header) == 0 || header[0] != '*' {
		return nil, errors.New("not an array")
	}
	count, err := strconv.Atoi(strings.TrimSpace(header[1:]))
	if err != nil {
		return nil, err
	}

	args := make([]string, 0, count)
	for range count {
		sizeLine, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(sizeLine[1:]))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2) // payload + CRLF
		if _, err := readFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}

	return args, nil
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	read := 0
	for read < len(buf) {
		n, err := r.Read(buf[read:])
		if err != nil {
			return read, err
		}
		read += n
	}

	return read, nil
}

func limiterAt(t *testing.T, s *redisServer, limits map[string]int) *Limiter {
	t.Helper()
	client, err := NewClient(s.addr, 2)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return NewLimiter(client, limits)
}
