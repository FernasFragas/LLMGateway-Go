package metrics

import (
	"context"
	"sync/atomic"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// AppDirectory counts resolved and unrecognized key lookups — the volume
// behind the refusal logs.NewAppDirectory writes.
type AppDirectory struct {
	app gateway.AppDirectory

	resolved atomic.Int64
	refused  atomic.Int64
}

// NewAppDirectory wraps next, counting every lookup it answers.
func NewAppDirectory(next gateway.AppDirectory) *AppDirectory {
	return &AppDirectory{app: next}
}

func (d *AppDirectory) AppForKey(ctx context.Context, apiKey string) (gateway.App, bool) {
	app, ok := d.app.AppForKey(ctx, apiKey)

	if ok {
		d.resolved.Add(1)
	} else {
		d.refused.Add(1)
	}

	return app, ok
}

// Resolved reports how many keys this instance attributed to an app.
func (d *AppDirectory) Resolved() int64 { return d.resolved.Load() }

// Refused reports how many keys nobody owned.
func (d *AppDirectory) Refused() int64 { return d.refused.Load() }
