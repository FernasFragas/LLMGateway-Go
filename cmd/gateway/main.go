// Command gateway runs the LLM gateway server. Config loading and the
// provider/limiter adapters land next; until they do, main stays empty and
// newServer — the composition — is exercised end to end by main_test.go.
package main

import (
	"log/slog"
	"net/http"

	"github.com/FernasFragas/LLMGateway-Go/internal/api"
	"github.com/FernasFragas/LLMGateway-Go/internal/health"
	apilogs "github.com/FernasFragas/LLMGateway-Go/internal/logs/api"
	"github.com/FernasFragas/LLMGateway-Go/internal/metrics"
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

func main() {}
