// Command gateway runs the LLM gateway server. run() composes the process
// from the pieces the rest of the tree already ships — config.Load, the
// identity chain (newAppDirectory + auth.KeepFresh), and newServer for the edge.
// newProviderClient builds the credential cache, the three dialect adapters,
// and the router that picks one per route; newLimiters builds both metered
// currencies. Every port the core declares now has a real adapter behind it,
// and each fails the way its own decision says it must: identity static,
// quotas open, slots exact.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/anthropic"
	"github.com/FernasFragas/LLMGateway-Go/internal/api"
	"github.com/FernasFragas/LLMGateway-Go/internal/auth"
	"github.com/FernasFragas/LLMGateway-Go/internal/config"
	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/health"
	"github.com/FernasFragas/LLMGateway-Go/internal/logs/api"
	authlogs "github.com/FernasFragas/LLMGateway-Go/internal/logs/auth"
	gwlogs "github.com/FernasFragas/LLMGateway-Go/internal/logs/gateway"
	keyslogs "github.com/FernasFragas/LLMGateway-Go/internal/logs/providerkeys"
	"github.com/FernasFragas/LLMGateway-Go/internal/metrics/api"
	"github.com/FernasFragas/LLMGateway-Go/internal/ollama"
	"github.com/FernasFragas/LLMGateway-Go/internal/openai"
	"github.com/FernasFragas/LLMGateway-Go/internal/providerkeys"
	"github.com/FernasFragas/LLMGateway-Go/internal/providers"
	"github.com/FernasFragas/LLMGateway-Go/internal/redis"
	"github.com/FernasFragas/LLMGateway-Go/internal/slots"
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
// JWKSCache is handed to auth.KeepFresh — the request path itself never touches
// the network.
func newAppDirectory(cfg config.Config, checker *health.Checker, log *slog.Logger) (gateway.AppDirectory, *auth.JWKSCache, error) {
	// In a pod this client trusts the cluster CA and carries the gateway's own
	// SA token to the RBAC-gated JWKS endpoint; outside one it is a plain
	// default client, so local dev stays unconfigured. The environment, not a
	// flag, selects the behavior.
	client, err := auth.NewFetchClient(auth.DefaultServiceAccountDir)
	if err != nil {
		return nil, nil, err
	}

	keys, err := auth.NewJWKSCache(cfg.JWKS.URL, client)
	if err != nil {
		return nil, nil, err
	}

	dir, err := auth.NewDirectory(cfg.Auth, keys)
	if err != nil {
		return nil, nil, err
	}

	checker.AddReadiness("jwks", keys.Ready)

	return gwlogs.NewAppDirectory(dir, log), keys, nil
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
	// Keep the key cache warm until ctx ends. The loop is auth's logic; main
	// only composes the logging decorator around the cache and hands it over.
	jwksRefresh := authlogs.NewJWKSRefresher(keys, log)
	go auth.KeepFresh(ctx, jwksRefresh, cfg.JWKS.RefreshInterval)

	provider, err := newProviderClient(ctx, cfg, checker, log)
	if err != nil {
		log.Error("failed to build provider client", "error", err)
		return err
	}

	// Both currencies, each in the store its failure mode demands: quotas in
	// Redis so an app's rps is one budget across replicas (and fails open when
	// it is down), slots in this process because a semaphore cannot fail and
	// the global cap's job is bounding this process's memory.
	quotas, slotting, err := newLimiters(cfg)
	if err != nil {
		log.Error("failed to build limiters", "error", err)
		return err
	}

	core, err := gateway.New(cfg.Gateway, gateway.Deps{
		Apps:        dir,
		RateLimiter: gwlogs.NewRateLimiter(quotas, log),
		Slots:       gwlogs.NewSlotLimiter(slotting, log),
		Provider:    gwlogs.NewProviderClient(provider, log),
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

// newProviderClient assembles the outbound half: the credential cache its
// sources describe, the three dialect adapters each holding an accessor into
// that cache rather than a copied string, and the router that picks one by the
// route's provider name.
//
// The cache's readiness joins the checker under its own name, beside "jwks":
// the two fail independently by design, and a stalled secret store must not
// mark the signing keys unready (decision #1). Its refresh loops run per
// provider, each on its own cadence.
func newProviderClient(ctx context.Context, cfg config.Config, checker *health.Checker, log *slog.Logger) (gateway.ProviderClient, error) {
	fetcher, err := newFetcher(cfg.SecretSource)
	if err != nil {
		return nil, err
	}

	sources := make(map[string]providerkeys.Source, len(cfg.SecretSource.Providers))
	for provider, src := range cfg.SecretSource.Providers {
		sources[provider] = providerkeys.Source{Path: src.Path, RefreshInterval: src.RefreshInterval}
	}

	keys, err := providerkeys.New(fetcher, sources)
	if err != nil {
		return nil, err
	}
	checker.AddReadiness("provider-keys", keys.Ready)

	// Every background refresh — periodic and post-rejection — runs through
	// the logging decorator; the cache's loops own the cadence, main only
	// injects what they call.
	keys.RefreshVia(keyslogs.NewRefresher(keys, log))

	// Load once before serving, then per provider on its own timer. A failed
	// first load is not fatal: readiness holds the pod out of rotation, and
	// the loops keep trying — a secret store that is late must not turn into a
	// crash loop.
	if err := keys.RefreshAll(ctx); err != nil {
		log.Warn("initial provider key load incomplete; readiness gates until a source answers", "error", err)
	}
	for _, provider := range keys.Providers() {
		go keys.KeepFresh(ctx, provider)
	}

	// Each adapter is handed an accessor, never a string: a key rotates under
	// a running pod, and a copy taken here would freeze at boot (ADR-001).
	client := &http.Client{}
	return providers.NewRouter(map[string]gateway.ProviderClient{
		"openai":    openai.NewOpenAI(client, keys.Key("openai")),
		"anthropic": anthropic.NewAnthropic(client, keys.Key("anthropic")),
		"ollama":    ollama.NewOllama(client, keys.Key("ollama")),
	}, func(provider string) {
		// Decision #8: the rejection is evidence the cache is stale. This
		// returns immediately — the refresh happens off the request path.
		if keys.TriggerRefresh(provider) {
			log.Warn("provider refused our credential; refreshing out of band", "provider", provider)
		}
	})
}

// newFetcher picks how credentials are read. Both kinds ship; an unknown kind
// never reaches here, the config loader having refused the file already.
func newFetcher(src config.SecretSource) (providerkeys.Fetcher, error) {
	switch src.Kind {
	case "vault":
		return providerkeys.NewVault(nil, providerkeys.VaultConfig{
			Address:  src.Vault.Address,
			Role:     src.Vault.Role,
			AuthPath: src.Vault.AuthPath,
		})
	case "file":
		return providerkeys.NewFile(), nil
	default:
		// No kind and no providers: every route is self-hosted and there is
		// nothing to read. providerkeys.New rejects the contradictory case.
		return nil, nil
	}
}

// newLimiters builds both metered currencies from the app blocks. Zero in any
// limit means that currency is unmetered for that app — the convention
// global_max_in_flight already uses, applied to both limiters so one caller
// cannot be treated inconsistently across currencies.
//
// The Redis client dials lazily: a quota store that is down must not stop the
// gateway from booting, because rate limiting fails open and identity does not
// depend on it (decision #1).
func newLimiters(cfg config.Config) (gateway.RateLimiter, gateway.SlotLimiter, error) {
	rps := make(map[string]int, len(cfg.Limits))
	ceilings := make(map[string]int, len(cfg.Limits))
	for app, limits := range cfg.Limits {
		rps[app] = limits.RPS
		ceilings[app] = limits.MaxInFlight
	}

	client, err := redis.NewClient(cfg.Redis.Addr, redisPoolSize)
	if err != nil {
		return nil, nil, err
	}

	return redis.NewLimiter(client, rps), slots.New(cfg.GlobalMaxInFlight, ceilings), nil
}

// redisPoolSize is small on purpose: the brief budgets ~3 ops per request
// against a store nowhere near its limits, so connections are reused rather
// than multiplied.
const redisPoolSize = 8
