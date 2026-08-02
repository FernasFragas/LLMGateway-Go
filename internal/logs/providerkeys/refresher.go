package logs

import (
	"context"
	"log/slog"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"

	providerkeys "github.com/FernasFragas/LLMGateway-Go/internal/providerkeys"
)

// Refresher logs a failed refresh of a provider's credential. The cache keeps
// serving the last known good key (fail static), so nothing else surfaces the
// degradation — this line is the operator's only signal, and the error still
// passes through unchanged for the caller to count or back off on. "provider
// key", never just "key": a reader must not have to guess whether a
// failed-refresh line came from this cache or from internal/auth's JWKS one.
type Refresher struct {
	next providerkeys.Refresher
	log  *slog.Logger
}

// NewRefresher wraps next; a nil log means slog.Default().
func NewRefresher(next providerkeys.Refresher, log *slog.Logger) *Refresher {
	return &Refresher{next: next, log: logs.OrDefault(log)}
}

func (r *Refresher) Refresh(ctx context.Context, provider string) error {
	err := r.next.Refresh(ctx, provider)
	if err != nil {
		r.log.WarnContext(ctx, "provider key refresh failed; serving the cached credential",
			"provider", provider, "err", err.Error())
	}

	return err
}
