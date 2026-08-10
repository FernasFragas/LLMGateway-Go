package redis

// Test harness: a fake Redis and the scenario builders both limiters share.
// Builders hide mechanics, never meaning — every rule under test stays visible
// at its call site.

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// redisServer speaks just enough RESP to answer both limiters: it parses the
// command array and answers EVAL from a per-key counter, reading the script
// to decide whether the call counts, debits, or only reports.
type redisServer struct {
	addr     string
	replyErr string

	// requirePass models a server started with --requirepass: every command
	// before a successful AUTH is refused with NOAUTH, which is what a
	// client that skips the handshake actually meets.
	requirePass string
	// roots verifies this server's certificate when it speaks TLS.
	roots *x509.CertPool

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

// serverOptions selects which connection-setup behavior the fake models.
// The zero value is the plaintext, password-less server every existing test
// builds against.
type serverOptions struct {
	requirePass string
	tls         bool
}

func fakeRedis(t *testing.T) *redisServer {
	t.Helper()

	return fakeRedisWith(t, serverOptions{})
}

func fakeRedisWith(t *testing.T, opts serverOptions) *redisServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	s := &redisServer{addr: ln.Addr().String(), counts: map[string]int64{}, requirePass: opts.requirePass}

	if opts.tls {
		cert, roots := selfSigned(t)
		s.roots = roots
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
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
	authed := s.requirePass == ""

	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}

		if len(args) > 0 && strings.EqualFold(args[0], "AUTH") {
			_, _ = c.Write([]byte(s.authReply(args)))
			if s.authReply(args) == "+OK\r\n" {
				authed = true
			}
			continue
		}
		if !authed {
			_, _ = c.Write([]byte("-NOAUTH Authentication required.\r\n"))
			continue
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

// authReply answers AUTH the way a real server does, including the two
// operator mistakes that must stay distinguishable: the wrong password, and
// a password sent to a server that has none.
func (s *redisServer) authReply(args []string) string {
	switch {
	case s.requirePass == "":
		return "-ERR Client sent AUTH, but no password is set\r\n"
	case len(args) == 2 && args[1] == s.requirePass:
		return "+OK\r\n"
	default:
		return "-WRONGPASS invalid username-password pair\r\n"
	}
}

// selfSigned mints a certificate for 127.0.0.1 and the pool that trusts it,
// so a TLS test verifies a real chain rather than skipping verification —
// which would leave the one thing worth proving untested.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"127.0.0.1"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, roots
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
	client, err := NewClient(s.addr, 2, Options{})
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
	client, err := NewClient("127.0.0.1:1", 2, Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}
