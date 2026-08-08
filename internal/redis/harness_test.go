package redis

// Test harness: a fake Redis and the scenario builders both limiters share.
// Builders hide mechanics, never meaning — every rule under test stays visible
// at its call site.

import (
	"bufio"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// redisServer speaks just enough RESP to answer both limiters: it parses the
// command array and answers EVAL from a per-key counter, reading the script
// to decide whether the call counts, debits, or only reports.
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

// spend reports what a key holds, so a test can assert on the counter itself
// rather than only on the verdict it produced.
func (s *redisServer) spend(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.counts[key]
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
		n := s.apply(args)
		s.mu.Unlock()

		_, _ = c.Write([]byte(":" + strconv.FormatInt(n, 10) + "\r\n"))
	}
}

// apply runs one command against the counters. Callers hold the lock.
//
// args are EVAL <script> 1 <key> [argv...]; which script it is decides the
// arithmetic, the same way the real server would.
func (s *redisServer) apply(args []string) int64 {
	if len(args) < 4 {
		return 0
	}
	script, key := args[1], args[3]

	switch {
	case strings.Contains(script, "INCRBY"): // token debit: EVAL script 1 key tokens ttl
		if len(args) < 5 {
			return 0
		}
		tokens, _ := strconv.ParseInt(args[4], 10, 64)
		s.counts[key] += tokens
	case strings.Contains(script, "GET"): // token check: read only
	default: // request-rate increment: EVAL script 1 key ttl
		s.counts[key]++
	}

	return s.counts[key]
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

func clientFor(t *testing.T, s *redisServer) *Client {
	t.Helper()
	client, err := NewClient(s.addr, 2)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

func limiterAt(t *testing.T, s *redisServer, limits map[string]int) *Limiter {
	t.Helper()

	return NewLimiter(clientFor(t, s), limits)
}

func tokenLimiterAt(t *testing.T, s *redisServer, budgets map[string]int) *TokenLimiter {
	t.Helper()

	return NewTokenLimiter(clientFor(t, s), budgets)
}

// deadStore points a client at a port nothing listens on, so every call fails
// the way an unreachable Redis does.
func deadStore(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient("127.0.0.1:1", 2)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}
