package logs

import (
	"context"
	"log/slog"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

var _ gateway.AppDirectory = (*AppDirectory)(nil)

// AppDirectory logs refusals of keys nobody owns — the only failure the core
// cannot attribute to an app. The key itself is a secret and is never logged;
// a successful resolution is logged not at all (the completion log already
// names the app).
type AppDirectory struct {
	app gateway.AppDirectory
	log *slog.Logger
}

// NewAppDirectory wraps next; a nil log means slog.Default().
func NewAppDirectory(next gateway.AppDirectory, log *slog.Logger) *AppDirectory {
	return &AppDirectory{app: next, log: logs.OrDefault(log)}
}

func (d *AppDirectory) AppForKey(ctx context.Context, apiKey string) (gateway.App, bool) {
	app, ok := d.app.AppForKey(ctx, apiKey)
	if !ok {
		d.log.WarnContext(ctx, "request refused: api key not recognized")
	}

	return app, ok
}
