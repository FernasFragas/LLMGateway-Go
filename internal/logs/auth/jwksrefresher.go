package logs

import (
	"context"
	"log/slog"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"
)

// refresher is the background key refresh as main drives it — structurally
// auth.KeyCache's Refresh.
type refresher interface {
	Refresh(ctx context.Context) error
}

var _ refresher = (*KeyRefresher)(nil)

// KeyRefresher logs a failed ServiceAccount key refresh. The cache keeps
// serving its stale keys (fail static), so nothing else surfaces the
// degradation — this line is the operator's only signal, and the error still
// passes through unchanged for the caller to count or back off on.
type KeyRefresher struct {
	next refresher
	log  *slog.Logger
}

// NewKeyRefresher wraps next; a nil log means slog.Default().
func NewKeyRefresher(next refresher, log *slog.Logger) *KeyRefresher {
	return &KeyRefresher{next: next, log: logs.OrDefault(log)}
}

func (r *KeyRefresher) Refresh(ctx context.Context) error {
	err := r.next.Refresh(ctx)
	if err != nil {
		r.log.WarnContext(ctx, "service-account key refresh failed; serving cached keys", "err", err.Error())
	}

	return err
}
