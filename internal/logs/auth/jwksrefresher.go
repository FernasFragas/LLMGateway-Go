package logs

import (
	"context"
	"log/slog"

	"github.com/FernasFragas/LLMGateway-Go/internal/logs"

	"github.com/FernasFragas/LLMGateway-Go/internal/auth"
)

// JWKSRefresher logs a failed refresh of the cluster's token-signing keys.
// The cache keeps verifying against its stale keys (fail static), so nothing
// else surfaces the degradation — this line is the operator's only signal,
// and the error still passes through unchanged for the caller to count or
// back off on. The provider key cache gets its own decorator; a reader must
// never have to guess which cache a "key refresh failed" line came from.
type JWKSRefresher struct {
	next auth.Refresher
	log  *slog.Logger
}

// NewJWKSRefresher wraps next; a nil log means slog.Default().
func NewJWKSRefresher(next auth.Refresher, log *slog.Logger) *JWKSRefresher {
	return &JWKSRefresher{next: next, log: logs.OrDefault(log)}
}

func (r *JWKSRefresher) Refresh(ctx context.Context) error {
	err := r.next.Refresh(ctx)
	if err != nil {
		r.log.WarnContext(ctx, "jwks refresh failed; verifying against cached signing keys", "err", err.Error())
	}

	return err
}
