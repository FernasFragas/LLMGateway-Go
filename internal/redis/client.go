// Package redis is the quota store adapter: fixed-window counters shared by
// every replica, so an app's 60 rps is one budget across the deployment
// rather than 60 per pod. It holds quota and breaker state only — never a
// key. That separation is decision #1: auth fails static, rate limiting fails
// open, and behind one store a single outage would trigger both policies with
// fail-closed winning, putting the asymmetry out of reach.
//
// The client is hand-rolled against RESP because this module keeps its
// runtime dependencies few and the surface actually needed is small: send an
// array of bulk strings, read one reply. A full client would bring
// pipelining, pub/sub, cluster routing, and sentinel support that nothing
// here uses. The trade is explicit — if this gateway ever needs cluster mode
// or pub/sub, take the dependency rather than growing this file.
//
// Hand-rolled buys a smaller binary, not a smaller obligation: this file owes
// a real server the agreement a library would have brought with it. The
// build-tagged suite in integration_test.go is where that debt is paid —
// nil-vs-integer reply framing, script atomicity, and pooled reuse after an
// error reply are all things the in-package fake cannot judge.
package redis

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// dialTimeout bounds establishing a connection. Every call also carries the
// caller's context, so this only covers the case where no deadline was set.
const dialTimeout = 2 * time.Second

// Options are the connection's security settings — everything that must be
// established before a pooled connection is usable, and nothing that varies
// per command. A zero Options is a plaintext, unauthenticated connection:
// correct for an in-cluster Redis on the pod network, and the reason those
// two fields have to be set deliberately rather than defaulted on.
type Options struct {
	// TLS wraps the connection once it is dialed. Required by managed caches
	// with encryption in transit enabled.
	TLS bool
	// RootCAs verifies the server's certificate; nil means the host's own
	// trust store. A managed cache fronted by a private CA needs its bundle
	// here, or the handshake fails with an unknown authority.
	RootCAs *x509.CertPool
	// Password is sent as AUTH on each new connection. Empty means the
	// server has no password — sending AUTH to a server without one is an
	// error, so this is not a value that can be defaulted to something safe.
	Password string
}

// Client is a minimal RESP client over a small connection pool. The zero
// value is not usable; call NewClient.
type Client struct {
	addr string
	opts Options
	// serverName is the host half of addr, used to verify the certificate.
	// Derived once rather than per dial, and separate from addr because a
	// certificate names a host, not a host:port.
	serverName string
	pool       chan *conn
}

type conn struct {
	net.Conn
	r *bufio.Reader
}

// NewClient prepares a client for addr with at most size pooled connections.
// Nothing is dialed here: a quota store that is down must not stop the
// gateway from booting, because rate limiting fails open and auth does not
// depend on it. That includes a wrong password or an untrusted certificate —
// both surface as connection errors on the first request, which the limiters
// report and the core fails open on, exactly like an unreachable host.
func NewClient(addr string, size int, opts Options) (*Client, error) {
	if addr == "" {
		return nil, errors.New("redis: address is required")
	}
	if size <= 0 {
		size = 4
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("redis: address %q must be host:port: %w", addr, err)
	}

	return &Client{addr: addr, opts: opts, serverName: host, pool: make(chan *conn, size)}, nil
}

// Int sends one command and reads an integer reply — the only reply shape the
// limiter needs. A connection that errors is dropped rather than returned to
// the pool: half a reply left in its buffer would corrupt every later call.
func (c *Client) Int(ctx context.Context, args ...string) (int64, error) {
	cn, err := c.take(ctx)
	if err != nil {
		return 0, err
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = cn.SetDeadline(deadline)
	} else {
		_ = cn.SetDeadline(time.Now().Add(dialTimeout))
	}

	n, err := cn.roundTrip(args)
	if err != nil {
		_ = cn.Close()
		return 0, err
	}
	c.put(cn)

	return n, nil
}

// Close drops every pooled connection.
func (c *Client) Close() error {
	for {
		select {
		case cn := <-c.pool:
			_ = cn.Close()
		default:
			return nil
		}
	}
}

func (c *Client) take(ctx context.Context) (*conn, error) {
	select {
	case cn := <-c.pool:
		return cn, nil
	default:
	}

	return c.dial(ctx)
}

// dial establishes one connection all the way to usable: TCP, then the TLS
// handshake, then AUTH. Every step is bounded by one deadline — a hung
// handshake or an unanswered AUTH would otherwise hold a pool slot with no
// command outstanding to time it out, which is a stall no caller can see.
//
// A connection that fails any step is closed rather than returned. Half a
// handshake or an unanswered AUTH leaves bytes nobody will read, and a
// connection carrying those would corrupt the first real command placed on
// it.
func (c *Client) dial(ctx context.Context) (*conn, error) {
	dialer := net.Dialer{Timeout: dialTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("redis: dial %s: %w", c.addr, err)
	}

	if err := raw.SetDeadline(setupDeadline(ctx)); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("redis: set deadline: %w", err)
	}

	if c.opts.TLS {
		tlsConn := tls.Client(raw, &tls.Config{
			ServerName: c.serverName,
			RootCAs:    c.opts.RootCAs,
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("redis: tls handshake %s: %w", c.addr, err)
		}
		raw = tlsConn
	}

	cn := &conn{Conn: raw, r: bufio.NewReader(raw)}

	if c.opts.Password != "" {
		if err := cn.auth(c.opts.Password); err != nil {
			_ = cn.Close()
			return nil, err
		}
	}

	// Clear the setup deadline: Int sets its own per command, and leaving
	// this one would expire a pooled connection mid-request later.
	if err := cn.SetDeadline(time.Time{}); err != nil {
		_ = cn.Close()
		return nil, fmt.Errorf("redis: clear deadline: %w", err)
	}

	return cn, nil
}

// setupDeadline bounds the handshake and AUTH by whichever is sooner: the
// caller's own deadline, or the same budget a dial gets.
func setupDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(dialTimeout)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		return caller
	}

	return deadline
}

func (c *Client) put(cn *conn) {
	select {
	case c.pool <- cn:
	default:
		_ = cn.Close() // pool full: this one is surplus
	}
}

// auth authenticates this connection before any command runs on it. AUTH is
// the one command here that answers with a simple string rather than an
// integer, which is why readStatus exists at all — the limiters need no
// other reply shape.
//
// A refusal travels with the server's own message: WRONGPASS and "Client
// sent AUTH, but no password is set" are different operator mistakes, and
// collapsing them into "auth failed" would hide which one was made.
func (cn *conn) auth(password string) error {
	if err := cn.write([]string{"AUTH", password}); err != nil {
		return err
	}

	return cn.readStatus()
}

// roundTrip writes one command as a RESP array of bulk strings and reads the
// integer reply.
func (cn *conn) roundTrip(args []string) (int64, error) {
	if err := cn.write(args); err != nil {
		return 0, err
	}

	return cn.readInt()
}

// write encodes one command as a RESP array of bulk strings and sends it.
func (cn *conn) write(args []string) error {
	var req []byte
	req = append(req, '*')
	req = strconv.AppendInt(req, int64(len(args)), 10)
	req = append(req, '\r', '\n')
	for _, arg := range args {
		req = append(req, '$')
		req = strconv.AppendInt(req, int64(len(arg)), 10)
		req = append(req, '\r', '\n')
		req = append(req, arg...)
		req = append(req, '\r', '\n')
	}

	if _, err := cn.Write(req); err != nil {
		return fmt.Errorf("redis: write: %w", err)
	}

	return nil
}

// readStatus reads one simple-string reply, translating the error form into
// a Go error the same way readInt does.
func (cn *conn) readStatus() error {
	line, err := cn.readLine()
	if err != nil {
		return err
	}
	if len(line) == 0 {
		return errors.New("redis: empty reply")
	}

	switch line[0] {
	case '+':
		return nil
	case '-':
		return fmt.Errorf("redis: %s", line[1:])
	default:
		return fmt.Errorf("redis: unexpected reply %q", line)
	}
}

// readInt reads one reply, accepting the integer form and translating the
// error form into a Go error. Anything else means the command was not the one
// this client is for.
func (cn *conn) readInt() (int64, error) {
	line, err := cn.readLine()
	if err != nil {
		return 0, err
	}
	if len(line) == 0 {
		return 0, errors.New("redis: empty reply")
	}

	switch line[0] {
	case ':':
		n, err := strconv.ParseInt(string(line[1:]), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("redis: unparseable integer reply %q", line)
		}
		return n, nil
	case '-':
		return 0, fmt.Errorf("redis: %s", line[1:])
	default:
		return 0, fmt.Errorf("redis: unexpected reply %q", line)
	}
}

// readLine reads one CRLF-terminated protocol line, without its terminator.
func (cn *conn) readLine() ([]byte, error) {
	line, err := cn.r.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("redis: read: %w", err)
	}
	if len(line) < 2 {
		return nil, errors.New("redis: truncated reply")
	}

	return line[:len(line)-2], nil
}
