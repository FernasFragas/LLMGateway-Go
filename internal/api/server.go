package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// ChatService is the one slice of the core this adapter drives —
// *gateway.Service satisfies it; tests substitute a stub.
type ChatService interface {
	Chat(ctx context.Context, apiKey string, req gateway.ChatRequest) (gateway.ChatResult, error)
}

// HealthChecker answers the two probe questions — *health.Checker satisfies
// it; a logging decorator wraps it in main so the refusal reason, which the
// terse probe body drops, still reaches an operator.
type HealthChecker interface {
	Live() error
	Ready(ctx context.Context) error
}

// Middleware is one link of a decorator chain around a handler. The seams in
// Deps carry them outermost-first; routes() states where each seam is spliced.
type Middleware func(http.Handler) http.Handler

// Config is the server's transport tuning. Zero values take the defaults
// below; the gateway config file is parsed elsewhere and lands here.
type Config struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// ReadHeaderTimeout bounds a client's headers — the cheap slow-loris guard.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds reading the whole request.
	ReadTimeout time.Duration
	// WriteTimeout bounds writing the response. It must exceed the largest
	// app TotalDeadline, or the server kills completions the core still
	// considers within budget.
	WriteTimeout time.Duration
	// IdleTimeout bounds keep-alive connections between requests.
	IdleTimeout time.Duration
	// MaxBodyBytes caps the request body; over it the caller gets a 413
	// (payload_too_large), never a dropped connection.
	MaxBodyBytes int64
}

// Deps are what the server drives: the core's chat service and the probe
// checker are required; the rest have safe defaults. This package never
// logs — observability arrives through the two middleware seams and the
// decorated ports, all composed in main.
type Deps struct {
	Chat   ChatService
	Health HealthChecker
	// ErrorLog is handed to http.Server verbatim (main builds it via
	// slog.NewLogLogger); nil means the stdlib default.
	ErrorLog *log.Logger
	// RequestMiddleware wraps the data path outside recoverPanic, outermost
	// first: request loggers and meters that must see every response,
	// including the 500 a recovered panic writes.
	RequestMiddleware []Middleware
	// PanicMiddleware sits just inside recoverPanic, outermost first, on
	// every route: decorators that observe a panic and re-panic. Only
	// recoverPanic, outermost of them all, swallows and answers.
	PanicMiddleware []Middleware
}

// Server is the HTTP front door: it owns the listener lifecycle and the
// routing table, and nothing about any one endpoint — handlers live in
// handler.go, cross-cutting concerns in middleware.go.
type Server struct {
	srv    *http.Server
	mux    *http.ServeMux
	health HealthChecker

	chat         ChatService
	requestMW    []Middleware
	panicMW      []Middleware
	maxBodyBytes int64

	mu   sync.Mutex
	addr string // actual bound address, set once Start listens
}

func New(cfg Config, deps Deps) (*Server, error) {
	switch {
	case deps.Chat == nil:
		return nil, errors.New("api: ChatService is required")
	case deps.Health == nil:
		return nil, errors.New("api: HealthChecker is required")
	}

	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = defaultReadTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultMaxBodyBytes
	}

	s := &Server{
		mux:          http.NewServeMux(),
		health:       deps.Health,
		chat:         deps.Chat,
		requestMW:    deps.RequestMiddleware,
		panicMW:      deps.PanicMiddleware,
		maxBodyBytes: cfg.MaxBodyBytes,
	}
	s.routes()

	s.srv = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          deps.ErrorLog, // nil means the stdlib default
	}

	return s, nil
}

// routes is the whole routing table on one screen: every endpoint, and
// exactly which middleware it goes through, stated explicitly — no route
// gets a layer it didn't ask for.
func (s *Server) routes() {
	// The data path gets the full chain, outermost first: correlate, observe
	// the request (the seam sits outside recoverPanic so it sees every
	// response, panic 500s included), answer panics — with the panic seam
	// just inside, seeing the panic itself before this recover swallows
	// it — cap the body, extract the credential. CORS and rate limiting are
	// absent by decision: the gateway is in-cluster only, and limits are
	// core business rules — new edge concerns slot in here.
	chain := func(h http.HandlerFunc) http.Handler {
		return s.correlationID(
			spliced(s.requestMW,
				s.recoverPanic(
					spliced(s.panicMW,
						s.maxBody(
							s.authenticate(h))))))
	}

	// Probes carry no credential (the kubelet won't send one) and skip the
	// request seam (they would drown the request log) — but panics are
	// still observed and answered, and every response still carries a
	// correlation ID. Metrics are not served here at all: per ADR-002 they
	// leave via OTLP push, wired directly in main, never through this mux.
	ops := func(h http.HandlerFunc) http.Handler {
		return s.correlationID(s.recoverPanic(spliced(s.panicMW, h)))
	}

	s.mux.Handle("POST /v1/chat", chain(s.handleChat))
	s.mux.Handle("GET /healthz", ops(s.handleHealthz))
	s.mux.Handle("GET /readyz", ops(s.handleReadyz))
}

// spliced layers mw around h, outermost first — spliced(mw, h) with
// mw = [a, b] serves as a(b(h)).
func spliced(mw []Middleware, h http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}

	return h
}

// ServeHTTP makes the Server itself an http.Handler, delegating to the
// routing table — tests exercise the full middleware chain without a socket.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Start binds the configured address and serves until Shutdown; a graceful
// shutdown returns nil. It blocks — run it in the main goroutine and let a
// signal handler call Shutdown.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("api: listen %s: %w", s.srv.Addr, err)
	}

	s.mu.Lock()
	s.addr = ln.Addr().String()
	s.mu.Unlock()

	if err := s.srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("api: serve: %w", err)
	}

	return nil
}

// Addr reports the bound address once Start has listened ("" before) — it
// lets callers, and tests, bind ":0" and discover the port.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.addr
}

// Shutdown stops accepting connections and drains in-flight requests until
// ctx expires. The caller picks the drain budget; it should exceed
// WriteTimeout or in-flight completions are cut off mid-answer.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
