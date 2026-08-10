package redis

// Connection setup: what has to be true before a pooled connection carries
// its first command. These are the clauses that let this client talk to a
// managed cache — and the ones whose absence fails silently, because quotas
// fail open and an unusable store reads as "no limits" rather than as an
// outage.

import (
	"context"
	"strings"
	"testing"
)

func TestAPasswordProtectedStoreIsUsableOnceAuthenticated(t *testing.T) {
	store := fakeRedisWith(t, serverOptions{requirePass: "s3cret"})
	l := NewLimiter(clientWith(t, store, Options{Password: "s3cret"}), map[string]int{"rag-api": 10})

	if d, err := l.Allow(context.Background(), "rag-api"); err != nil || !d.Allowed {
		t.Fatalf("Allow against an authenticated connection: %+v %v", d, err)
	}
}

func TestSkippingAuthAgainstAProtectedStoreIsReportedNotAdmitted(t *testing.T) {
	// The misconfiguration this whole change exists to make loud. Without a
	// password the server refuses every command with NOAUTH; the limiter must
	// report that as a store failure so the core fails open *and records the
	// degradation*, rather than the quota quietly never applying.
	store := fakeRedisWith(t, serverOptions{requirePass: "s3cret"})
	l := NewLimiter(clientWith(t, store, Options{}), map[string]int{"rag-api": 10})

	d, err := l.Allow(context.Background(), "rag-api")
	if err == nil {
		t.Fatal("a store refusing NOAUTH must be reported, so the core can fail open and record it")
	}
	if !strings.Contains(err.Error(), "NOAUTH") {
		t.Errorf("error = %v, want the server's own NOAUTH carried through", err)
	}
	if d.Allowed {
		t.Error("the limiter must not decide to admit; that call belongs to the core")
	}
}

func TestAWrongPasswordTravelsWithTheServersOwnMessage(t *testing.T) {
	// WRONGPASS and "no password is set" are different operator mistakes.
	// Collapsing them into "auth failed" would hide which one was made.
	store := fakeRedisWith(t, serverOptions{requirePass: "s3cret"})
	l := NewLimiter(clientWith(t, store, Options{Password: "wrong"}), map[string]int{"rag-api": 10})

	_, err := l.Allow(context.Background(), "rag-api")
	if err == nil || !strings.Contains(err.Error(), "WRONGPASS") {
		t.Errorf("error = %v, want WRONGPASS carried through", err)
	}
}

func TestAPasswordSentToAStoreWithoutOneIsReported(t *testing.T) {
	// The inverse misconfiguration, and the reason Password cannot simply be
	// defaulted on: AUTH against a server with no password is an error, so a
	// "safe default" would break every working plaintext deployment.
	store := fakeRedis(t)
	l := NewLimiter(clientWith(t, store, Options{Password: "s3cret"}), map[string]int{"rag-api": 10})

	_, err := l.Allow(context.Background(), "rag-api")
	if err == nil || !strings.Contains(err.Error(), "no password is set") {
		t.Errorf("error = %v, want the server's own complaint carried through", err)
	}
}

func TestATLSStoreIsUsableWhenItsCertificateIsTrusted(t *testing.T) {
	store := fakeRedisWith(t, serverOptions{tls: true})
	l := NewLimiter(clientWith(t, store, Options{TLS: true, RootCAs: store.roots}), map[string]int{"rag-api": 10})

	if d, err := l.Allow(context.Background(), "rag-api"); err != nil || !d.Allowed {
		t.Fatalf("Allow over TLS: %+v %v", d, err)
	}
}

func TestAnUntrustedCertificateIsRefusedRatherThanAccepted(t *testing.T) {
	// The clause that makes TLS worth having. Verification is against the
	// configured bundle, and a client that would accept any certificate is
	// encrypting against whoever answers the address.
	store := fakeRedisWith(t, serverOptions{tls: true})
	l := NewLimiter(clientWith(t, store, Options{TLS: true}), map[string]int{"rag-api": 10})

	_, err := l.Allow(context.Background(), "rag-api")
	if err == nil {
		t.Fatal("an unverifiable certificate was accepted")
	}
	if !strings.Contains(err.Error(), "tls handshake") {
		t.Errorf("error = %v, want it named as a handshake failure so an operator reads the cause", err)
	}
}

func TestAPlaintextClientAgainstATLSStoreFailsRatherThanHanging(t *testing.T) {
	// A plaintext client sends its command as cleartext to a server waiting
	// for a ClientHello. Both sides then wait — which is why the setup
	// deadline exists rather than only the per-command one.
	store := fakeRedisWith(t, serverOptions{tls: true})
	l := NewLimiter(clientWith(t, store, Options{}), map[string]int{"rag-api": 10})

	if _, err := l.Allow(context.Background(), "rag-api"); err == nil {
		t.Fatal("a plaintext client against a TLS store must fail, not hang")
	}
}

func TestBothTLSAndAuthApplyToEveryNewConnection(t *testing.T) {
	// Setup happens per connection, not per client: a connection dropped
	// after an error is replaced by a fresh dial that must handshake and
	// authenticate again before it carries anything.
	store := fakeRedisWith(t, serverOptions{requirePass: "s3cret", tls: true})
	client := clientWith(t, store, Options{TLS: true, RootCAs: store.roots, Password: "s3cret"})
	l := NewLimiter(client, map[string]int{"rag-api": 1000})

	for i := range 5 {
		if d, err := l.Allow(context.Background(), "rag-api"); err != nil || !d.Allowed {
			t.Fatalf("call %d: %+v %v", i+1, d, err)
		}
	}
}

func TestAnAddressWithoutAPortIsRefusedAtConstruction(t *testing.T) {
	// The certificate names a host, so the host has to be separable from the
	// address. Catching it here makes a typo a boot failure rather than a
	// handshake error on the first refused request.
	if _, err := NewClient("redis.example.com", 2, Options{TLS: true}); err == nil {
		t.Error("an address with no port was accepted; it cannot yield a server name to verify")
	}
}

// clientWith builds a client for store under opts, so each test states only
// the setting it is about.
func clientWith(t *testing.T, s *redisServer, opts Options) *Client {
	t.Helper()
	client, err := NewClient(s.addr, 2, opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}
