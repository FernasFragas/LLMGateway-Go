package auth

import (
	"context"
	"errors"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

var _ gateway.AppDirectory = (*Directory)(nil)

// Config binds the verifier to one cluster and names the callers.
type Config struct {
	// Issuer is the cluster's ServiceAccount token issuer, matched exactly.
	Issuer string
	// Audience is this gateway's identity in the token's aud claim; tokens
	// projected for anything else are refused.
	Audience string
	// Apps maps a token subject — "system:serviceaccount:<namespace>:<name>"
	// — to the app whose terms the core enforces. A subject absent here is
	// unauthorized, however valid its token.
	Apps map[string]gateway.App
}

// Directory resolves a projected ServiceAccount token to its app: verify
// locally against the cached keys, then look the subject up. It answers from
// memory alone — fail static, exactly as AppDirectory demands.
type Directory struct {
	cfg  Config
	keys *JWKSCache
}

func NewDirectory(cfg Config, keys *JWKSCache) (*Directory, error) {
	switch {
	case cfg.Issuer == "":
		return nil, errors.New("auth: Issuer is required")
	case cfg.Audience == "":
		return nil, errors.New("auth: Audience is required")
	case len(cfg.Apps) == 0:
		return nil, errors.New("auth: at least one app must be named")
	case keys == nil:
		return nil, errors.New("auth: JWKSCache is required")
	}

	return &Directory{cfg: cfg, keys: keys}, nil
}

// AppForKey treats the presented credential as a bound ServiceAccount token.
// Any failure — bad signature, wrong audience, expired, unknown subject —
// is the same false: the port reports who the caller is or that nobody is,
// never why.
func (d *Directory) AppForKey(_ context.Context, token string) (gateway.App, bool) {
	subject, err := verifyToken(token, d.keys.key, d.cfg.Issuer, d.cfg.Audience, time.Now())
	if err != nil {
		return gateway.App{}, false
	}

	app, ok := d.cfg.Apps[subject]

	return app, ok
}
