// Package redis is the quota store adapter: fixed-window counters shared by
// every replica, so an app's 60 rps is one budget across the deployment
// rather than 60 per pod. It holds quota and breaker state only — never a
// key. That separation is decision #1: auth fails static, rate limiting fails
// open, and behind one store a single outage would trigger both policies with
// fail-closed winning, putting the asymmetry out of reach.
//
// The client is hand-rolled against RESP because this module has one
// dependency and the surface actually needed is small: send an array of bulk
// strings, read one reply. A full client would bring pipelining, pub/sub,
// cluster routing, and sentinel support that nothing here uses. The trade is
// explicit — if this gateway ever needs cluster mode or pub/sub, take the
// dependency rather than growing this file.
package redis

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// dialTimeout bounds establishing a connection. Every call also carries the
// caller's context, so this only covers the case where no deadline was set.
const dialTimeout = 2 * time.Second

// Client is a minimal RESP client over a small connection pool. The zero
// value is not usable; call NewClient.
type Client struct {
	addr string
	pool chan *conn
}

type conn struct {
	net.Conn
	r *bufio.Reader
}

// NewClient prepares a client for addr with at most size pooled connections.
// Nothing is dialed here: a quota store that is down must not stop the
// gateway from booting, because rate limiting fails open and auth does not
// depend on it.
func NewClient(addr string, size int) (*Client, error) {
	if addr == "" {
		return nil, errors.New("redis: address is required")
	}
	if size <= 0 {
		size = 4
	}

	return &Client{addr: addr, pool: make(chan *conn, size)}, nil
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

	dialer := net.Dialer{Timeout: dialTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("redis: dial %s: %w", c.addr, err)
	}

	return &conn{Conn: raw, r: bufio.NewReader(raw)}, nil
}

func (c *Client) put(cn *conn) {
	select {
	case c.pool <- cn:
	default:
		_ = cn.Close() // pool full: this one is surplus
	}
}

// roundTrip writes one command as a RESP array of bulk strings and reads the
// integer reply.
func (cn *conn) roundTrip(args []string) (int64, error) {
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
		return 0, fmt.Errorf("redis: write: %w", err)
	}

	return cn.readInt()
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
