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
	gwmetrics "github.com/FernasFragas/LLMGateway-Go/internal/metrics/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/metrics/otlp"
	keysmetrics "github.com/FernasFragas/LLMGateway-Go/internal/metrics/providerkeys"
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
func newAppDirectory(cfg config.Config, checker *health.Checker, log *slog.Logger) (gateway.AppDirectory, *auth.JWKSCache, *gwmetrics.AppDirectory, error) {
	// In a pod this client trusts the cluster CA and carries the gateway's own
	// SA token to the RBAC-gated JWKS endpoint; outside one it is a plain
	// default client, so local dev stays unconfigured. The environment, not a
	// flag, selects the behavior.
	client, err := auth.NewFetchClient(auth.DefaultServiceAccountDir)
	if err != nil {
		return nil, nil, nil, err
	}

	keys, err := auth.NewJWKSCache(cfg.JWKS.URL, client)
	if err != nil {
		return nil, nil, nil, err
	}

	dir, err := auth.NewDirectory(cfg.Auth, keys)
	if err != nil {
		return nil, nil, nil, err
	}

	checker.AddReadiness("jwks", keys.Ready)

	// Metrics innermost, logging outermost — the same seam newServer uses,
	// so the log line and the counter always observe the same lookup.
	metered := gwmetrics.NewAppDirectory(dir)
	return gwlogs.NewAppDirectory(metered, log), keys, metered, nil
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

	dir, keys, dirMetrics, err := newAppDirectory(cfg, checker, log)
	if err != nil {
		log.Error("failed to build app directory", "error", err)
		return err
	}
	// Keep the key cache warm until ctx ends. The loop is auth's logic; main
	// only composes the logging decorator around the cache and hands it over.
	jwksRefresh := authlogs.NewJWKSRefresher(keys, log)
	go auth.KeepFresh(ctx, jwksRefresh, cfg.JWKS.RefreshInterval)

	provider, providerMetrics, refreshMetrics, triggers, providerKeys, err := newProviderClient(ctx, cfg, checker, log)
	if err != nil {
		log.Error("failed to build provider client", "error", err)
		return err
	}

	// Every currency, each in the store its failure mode demands: rate quotas
	// and token budgets in Redis so an app's rps and tokens per minute are one
	// budget across replicas (and fail open when it is down), slots in this
	// process because a semaphore cannot fail and the global cap's job is
	// bounding this process's memory.
	quotas, budgets, slotting, err := newLimiters(cfg)
	if err != nil {
		log.Error("failed to build limiters", "error", err)
		return err
	}

	// Metrics innermost, logging outermost, on every port — the same seam
	// newServer already documents for the HTTP edge. UsageRecorder inverts
	// it only because logs.NewUsageRecorder is a terminal sink with no next
	// of its own: metrics wraps it instead, forwarding every call through.
	rateLimiterMetrics := gwmetrics.NewRateLimiter(quotas)
	tokenLimiterMetrics := gwmetrics.NewTokenLimiter(budgets)
	slotLimiterMetrics := gwmetrics.NewSlotLimiter(slotting)
	usageMetrics := gwmetrics.NewUsageRecorder(gwlogs.NewUsageRecorder(log))

	core, err := gateway.New(cfg.Gateway, gateway.Deps{
		Apps:        dir,
		RateLimiter: gwlogs.NewRateLimiter(rateLimiterMetrics, log),
		Tokens:      gwlogs.NewTokenLimiter(tokenLimiterMetrics, log),
		Slots:       gwlogs.NewSlotLimiter(slotLimiterMetrics, log),
		Provider:    provider,
		Usage:       usageMetrics,
	})
	if err != nil {
		log.Error("failed to build gateway core", "error", err)
		return err
	}

	// Per ADR-002, metrics leave via OTLP push to the collector, not a
	// scrape endpoint — there is no /metrics route to wire. The exporter
	// reads every counter above through observable instruments; a collector
	// that's unreachable degrades exports, never the request path.
	otlpProvider, err := otlp.NewProvider(ctx, cfg.Telemetry.OTLPEndpoint)
	if err != nil {
		log.Error("failed to build metrics exporter", "error", err)
		return err
	}
	requestMetrics, panicMetrics := metrics.NewRequestMetrics(), metrics.NewPanicCounter()
	if err := registerMetrics(otlpProvider, dirMetrics, rateLimiterMetrics, tokenLimiterMetrics, slotLimiterMetrics, providerMetrics, usageMetrics,
		refreshMetrics, triggers, providerKeys, slotting, cfg, requestMetrics, panicMetrics); err != nil {
		log.Error("failed to register metrics instruments", "error", err)
		return err
	}

	srv, err := newServer(cfg.Server, core, checker, log, requestMetrics, panicMetrics)
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

	// Flush whatever the last collection interval gathered; a lost final
	// export is not worth extending the drain budget over.
	if err := otlpProvider.Shutdown(shutdownCtx); err != nil {
		log.Error("metrics exporter shutdown failed", "error", err)
	}

	log.Info("gateway stopped")
	return nil
}

// registerMetrics attaches every port's observable instruments to the OTLP
// provider. main constructs every counter above; this is just the seam that
// hands them to the exporter, per the observability decorator rule — the
// exporter never reaches into gateway or providerkeys business logic.
func registerMetrics(p *otlp.Provider, dir *gwmetrics.AppDirectory, rl *gwmetrics.RateLimiter, tl *gwmetrics.TokenLimiter,
	sl *gwmetrics.SlotLimiter, pc *gwmetrics.ProviderClient, usage *gwmetrics.UsageRecorder,
	refresher *keysmetrics.Refresher, triggers *keysmetrics.TriggerCounter,
	keys *providerkeys.Cache, slotting *slots.Limiter, cfg config.Config, reqs *metrics.RequestMetrics, panics *metrics.PanicCounter,
) error {
	meter := p.Meter()

	if err := otlp.RegisterGateway(meter, dir, rl, tl, sl, pc, usage); err != nil {
		return err
	}
	if err := otlp.RegisterProviderKeys(meter, refresher, triggers, keys, keys.Providers()); err != nil {
		return err
	}

	apps := make([]string, 0, len(cfg.Limits))
	for app := range cfg.Limits {
		apps = append(apps, app)
	}
	if err := otlp.RegisterSlots(meter, slotting, apps); err != nil {
		return err
	}

	return otlp.RegisterAPI(meter, reqs, panics)
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
func newProviderClient(ctx context.Context, cfg config.Config, checker *health.Checker, log *slog.Logger) (
	gateway.ProviderClient, *gwmetrics.ProviderClient, *keysmetrics.Refresher, *keysmetrics.TriggerCounter, *providerkeys.Cache, error,
) {
	fetcher, err := newFetcher(cfg.SecretSource)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	sources := make(map[string]providerkeys.Source, len(cfg.SecretSource.Providers))
	for provider, src := range cfg.SecretSource.Providers {
		sources[provider] = providerkeys.Source{Path: src.Path, RefreshInterval: src.RefreshInterval}
	}

	keys, err := providerkeys.New(fetcher, sources)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	checker.AddReadiness("provider-keys", keys.Ready)

	// Every background refresh — periodic and post-rejection — runs through
	// metrics then logging: metrics innermost so the counter and the log
	// line observe the identical error.
	refreshMetrics := keysmetrics.NewRefresher(keys)
	keys.RefreshVia(keyslogs.NewRefresher(refreshMetrics, log))

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

	triggers := keysmetrics.NewTriggerCounter()

	// Each adapter is handed an accessor, never a string: a key rotates under
	// a running pod, and a copy taken here would freeze at boot (ADR-001).
	client := &http.Client{}
	router, err := providers.NewRouter(map[string]gateway.ProviderClient{
		"openai":    openai.NewOpenAI(client, keys.Key("openai")),
		"anthropic": anthropic.NewAnthropic(client, keys.Key("anthropic")),
		"ollama":    ollama.NewOllama(client, keys.Key("ollama")),
	}, func(provider string) {
		// Decision #8: the rejection is evidence the cache is stale. This
		// returns immediately — the refresh happens off the request path.
		if keys.TriggerRefresh(provider) {
			log.Warn("provider refused our credential; refreshing out of band", "provider", provider)
			triggers.Trigger(provider)
		}
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	// Metrics innermost, logging outermost — the same seam every other
	// gateway port uses.
	providerMetrics := gwmetrics.NewProviderClient(router)
	return gwlogs.NewProviderClient(providerMetrics, log), providerMetrics, refreshMetrics, triggers, keys, nil
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
func newLimiters(cfg config.Config) (*redis.Limiter, *redis.TokenLimiter, *slots.Limiter, error) {
	rps := make(map[string]int, len(cfg.Limits))
	budgets := make(map[string]int, len(cfg.Limits))
	ceilings := make(map[string]int, len(cfg.Limits))
	for app, limits := range cfg.Limits {
		rps[app] = limits.RPS
		budgets[app] = limits.TokensPerMinute
		ceilings[app] = limits.MaxInFlight
	}

	// Both rate currencies share one client: they are the same store, the same
	// fail-open policy, and the same key convention — only the window differs.
	client, err := redis.NewClient(cfg.Redis.Addr, redisPoolSize)
	if err != nil {
		return nil, nil, nil, err
	}

	return redis.NewLimiter(client, rps),
		redis.NewTokenLimiter(client, budgets),
		slots.New(cfg.GlobalMaxInFlight, ceilings),
		nil
}

// redisPoolSize is small on purpose: the brief budgets ~3 ops per request
// against a store nowhere near its limits — ~5 since the token currency
// landed (ADR-003) — so connections are reused rather than multiplied.
const redisPoolSize = 8
