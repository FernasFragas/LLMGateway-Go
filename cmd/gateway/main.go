// Command gateway runs the LLM gateway server. run() composes the process
// from the pieces the rest of the tree already ships — config.Load, the
// identity chain (newAppDirectory + refreshKeys), and newServer for the edge.
// The provider and limiter adapters do not exist yet, so run() wires named
// placeholders (see TODO step 0): with no model behind the core, a minted
// token earning a 502 instead of a 401 proves the entire auth path works.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/api"
	"github.com/FernasFragas/LLMGateway-Go/internal/auth"
	"github.com/FernasFragas/LLMGateway-Go/internal/config"
	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/health"
	"github.com/FernasFragas/LLMGateway-Go/internal/logs/api"
	authlogs "github.com/FernasFragas/LLMGateway-Go/internal/logs/auth"
	gwlogs "github.com/FernasFragas/LLMGateway-Go/internal/logs/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/metrics/api"
)

// newServer is the one place the layers meet. The api package never logs or
// counts — observability is decorators composed here, logging outermost then
// metrics, per seam:
//
//   - the request seam (outside the panic answer): logs.RequestLogger, then
//     metrics.RequestMetrics — both see every response, panic 500s included;
//   - the panic seam (just inside it): logs.PanicLogger, then
//     metrics.PanicCounter — each observes the panic and re-panics; only the
//     edge's recover, outermost of all, swallows and answers;
//   - the ports the edge consumes: logs.ChatService around the core,
//     logs.HealthChecker around the probes.
func newServer(cfg api.Config, core api.ChatService, checker *health.Checker,
	log *slog.Logger, reqs *metrics.RequestMetrics, panics *metrics.PanicCounter,
) (*api.Server, error) {
	return api.New(cfg, api.Deps{
		Chat:     logs.NewChatService(core, log),
		Health:   logs.NewHealthChecker(checker, log),
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError),
		RequestMiddleware: []api.Middleware{
			func(next http.Handler) http.Handler { return logs.NewRequestLogger(next, log) },
			reqs.Wrap,
		},
		PanicMiddleware: []api.Middleware{
			func(next http.Handler) http.Handler { return logs.NewPanicLogger(next, log) },
			panics.Wrap,
		},
	})
}

// newAppDirectory assembles the identity chain from the config: the key
// cache feeding the verifier, readiness registered fail-closed on the
// checker (a cold pod takes no traffic instead of refusing every token),
// and the unknown-caller log wrapped around the directory. The returned
// KeyCache is handed to refreshKeys — the request path itself never touches
// the network.
func newAppDirectory(cfg config.Config, checker *health.Checker, log *slog.Logger) (gateway.AppDirectory, *auth.KeyCache, error) {
	// In a pod this client trusts the cluster CA and carries the gateway's own
	// SA token to the RBAC-gated JWKS endpoint; outside one it is a plain
	// default client, so local dev stays unconfigured. The environment, not a
	// flag, selects the behavior.
	client, err := auth.NewFetchClient(auth.DefaultServiceAccountDir)
	if err != nil {
		return nil, nil, err
	}

	keys, err := auth.NewKeyCache(cfg.JWKS.URL, client)
	if err != nil {
		return nil, nil, err
	}

	dir, err := auth.NewDirectory(cfg.Auth, keys)
	if err != nil {
		return nil, nil, err
	}

	checker.AddReadiness("service-account-keys", keys.Ready)

	return gwlogs.NewAppDirectory(dir, log), keys, nil
}

// refreshKeys keeps the cache warm until ctx ends: once immediately, so
// readiness turns green as soon as the issuer answers, then every interval —
// each attempt through the logs decorator, because a failed refresh serves
// stale keys silently and the log line is the only signal.
func refreshKeys(ctx context.Context, keys *auth.KeyCache, interval time.Duration, log *slog.Logger) {
	refresh := authlogs.NewKeyRefresher(keys, log)
	_ = refresh.Refresh(ctx)

	tick := time.NewTicker(interval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = refresh.Refresh(ctx)
		}
	}
}

func main() {
	if err := run(); err != nil {
		// run() has already logged the specifics; this is the process's dying
		// word and its non-zero exit — the signal a supervisor reads.
		slog.Error("gateway exited", "error", err)
		os.Exit(1)
	}
}

// run is main() with an error to return, so every boot failure exits non-zero
// through one path instead of scattered os.Exit calls. It composes the
// process, serves until a signal arrives, then drains — and turns a bad
// config, an unbuildable identity chain, or a failed bind into that error.
func run() error {
	configPath := flag.String("config", "config/config.yaml", "path to the gateway config file")
	flag.Parse()

	// JSON on stdout because in a cluster the reader is Loki/CloudWatch, not a
	// human at a terminal; the handler is the base every decorator wraps.
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// A typo or missing file fails the boot here, non-zero — exactly what the
	// strict loader (KnownFields) was built to do, before anything is served.
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("failed to load config", "path", *configPath, "error", err)
		return err
	}

	// SIGINT/SIGTERM cancel ctx; Kubernetes sends SIGTERM on every rollout, so
	// this is what makes deployments zero-drop, not optional polish.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	checker := health.NewChecker()

	dir, keys, err := newAppDirectory(cfg, checker, log)
	if err != nil {
		log.Error("failed to build app directory", "error", err)
		return err
	}
	// Keep the key cache warm until ctx ends; the first refresh turns readiness
	// green as soon as the issuer answers.
	go refreshKeys(ctx, keys, cfg.JWKS.RefreshInterval, log)

	// The core, driven by named placeholders standing in for the adapters that
	// have not landed. Each is wrapped in its logs decorator so the
	// placeholder's behavior is as observable as the real adapter will be —
	// the seam does not change when the scaffolding is replaced.
	core, err := gateway.New(cfg.Gateway, gateway.Deps{
		Apps:        dir,
		RateLimiter: gwlogs.NewRateLimiter(allowAllLimiter{}, log),
		Slots:       gwlogs.NewSlotLimiter(unboundedSlots{}, log),
		Provider:    gwlogs.NewProviderClient(unreachableProvider{}, log),
		Usage:       gwlogs.NewUsageRecorder(log),
	})
	if err != nil {
		log.Error("failed to build gateway core", "error", err)
		return err
	}

	// The metrics counters are the in-memory RequestMetrics/PanicCounter the
	// edge already consumes; the OTLP/Prometheus provider and the /metrics
	// handler are not built yet, so api.Deps.Metrics stays nil and that route
	// 404s until the adapter lands.
	srv, err := newServer(cfg.Server, core, checker, log, metrics.NewRequestMetrics(), metrics.NewPanicCounter())
	if err != nil {
		log.Error("failed to build server", "error", err)
		return err
	}

	// Start binds and blocks; run it off the main goroutine so a signal can
	// reach the shutdown path. A bind failure lands on serveErr and ends run().
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Start() }()

	log.Info("gateway started",
		"listen", cfg.Server.Addr,
		"issuer", cfg.Auth.Issuer,
		"config", *configPath,
	)

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Info("shutdown initiated")
	}

	// The drain budget must exceed WriteTimeout, or an in-flight completion the
	// core still considers within budget gets cut off mid-answer. Config leaves
	// WriteTimeout zero (the server defaults it to 90s internally), so fall
	// back to a floor that clears that default.
	drain := defaultDrainTimeout
	if cfg.Server.WriteTimeout > 0 {
		drain = cfg.Server.WriteTimeout + drainGrace
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "error", err)
		return err
	}

	log.Info("gateway stopped")
	return nil
}

const (
	// defaultDrainTimeout clears the server's own 90s default WriteTimeout when
	// the config leaves it unset.
	defaultDrainTimeout = 100 * time.Second
	// drainGrace pads a configured WriteTimeout so shutdown outlives the
	// longest response the server itself would allow.
	drainGrace = 10 * time.Second
)

// ─── step-0 placeholders ────────────────────────────────────────────────────
//
// These are scaffolding with a purpose, not stubs to forget. gateway.New
// requires a ProviderClient and both limiters; none of those adapters exist
// yet. Wiring honest placeholders lets the whole identity path run in a
// cluster with no model behind it — a minted token reaching the core and
// earning a 502 (not a 401) is the proof the auth chain works end to end.
// Each is replaced, one at a time, by a real adapter behind the same port.

// unreachableProvider answers every attempt with an honest unreachable fault,
// so a fully authorized request surfaces as 502 upstream_failed rather than
// pretending to serve.
type unreachableProvider struct{}

func (unreachableProvider) Complete(context.Context, gateway.ModelProvider, gateway.ChatRequest) (gateway.Completion, error) {
	return gateway.Completion{}, &gateway.ProviderFault{
		Kind:  gateway.FaultUnreachable,
		Cause: errors.New("no provider adapter wired yet"),
	}
}

// allowAllLimiter admits every request: the rate/token quota adapter (Redis)
// has not landed, and admitting-everything is the correct scaffold — it never
// refuses a request the real limiter might have allowed.
type allowAllLimiter struct{}

func (allowAllLimiter) Allow(context.Context, string) (gateway.RateDecision, error) {
	return gateway.RateDecision{Allowed: true}, nil
}

// unboundedSlots hands out a slot to every caller: the in-flight limiter has
// not landed, and a never-full ceiling is the matching scaffold. release is a
// no-op because nothing is being counted.
type unboundedSlots struct{}

func (unboundedSlots) TryAcquire(string) (release func(), ceiling int, ok bool) {
	return func() {}, 0, true
}
