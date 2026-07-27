package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const defaultFetchTimeout = 10 * time.Second

// JWKSCache holds the cluster's ServiceAccount public keys, fetched from the
// JWKS endpoint and cached in memory. The request path only ever reads the
// cache; Refresh is the one method that touches the network, and a failed
// Refresh keeps the previous keys — stale-but-usable, never unavailable.
type JWKSCache struct {
	url    string
	client *http.Client

	mu   sync.RWMutex
	keys map[string]*rsa.PublicKey // by kid
}

// NewJWKSCache prepares a cache for the given JWKS URL; a nil client means a
// default one with a 10s timeout. The cache starts empty — not ready — until
// the first successful Refresh.
func NewJWKSCache(jwksURL string, client *http.Client) (*JWKSCache, error) {
	if jwksURL == "" {
		return nil, errors.New("auth: JWKS URL is required")
	}
	if client == nil {
		client = &http.Client{Timeout: defaultFetchTimeout}
	}

	return &JWKSCache{url: jwksURL, client: client}, nil
}

// jwksWire is the JWKS document, RSA members only — the rest are skipped,
// not rejected: a cluster may advertise keys this verifier will never need.
type jwksWire struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

// Refresh fetches the JWKS document and replaces the cached keys — only on
// success, and only when the document holds at least one usable key: an
// endpoint answering garbage must not empty a working cache.
func (c *JWKSCache) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return fmt.Errorf("auth: build JWKS request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("auth: fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: fetch JWKS: status %d", resp.StatusCode)
	}

	var wire jwksWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return fmt.Errorf("auth: decode JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(wire.Keys))
	for _, k := range wire.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		pub, err := rsaKey(k.N, k.E)
		if err != nil {
			return fmt.Errorf("auth: JWKS key %q: %w", k.Kid, err)
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("auth: JWKS document holds no usable RSA key")
	}

	c.mu.Lock()
	c.keys = keys
	c.mu.Unlock()

	return nil
}

// Ready is health.Check-shaped: a cache that has never loaded keys refuses
// readiness, the same fail-closed cold start the key cache had before.
func (c *JWKSCache) Ready(context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.keys) == 0 {
		return errors.New("no service-account keys loaded yet")
	}

	return nil
}

// key returns the cached public key for kid.
func (c *JWKSCache) key(kid string) (*rsa.PublicKey, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	pub, ok := c.keys[kid]

	return pub, ok
}

// rsaKey assembles a public key from the JWKS base64url big-endian fields.
func rsaKey(n, e string) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil {
		return nil, fmt.Errorf("modulus: %w", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(e)
	if err != nil {
		return nil, fmt.Errorf("exponent: %w", err)
	}
	if len(nb) == 0 || len(eb) == 0 {
		return nil, errors.New("empty modulus or exponent")
	}

	exp := new(big.Int).SetBytes(eb)
	if !exp.IsInt64() || exp.Int64() > int64(^uint32(0)) {
		return nil, errors.New("exponent out of range")
	}

	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(exp.Int64())}, nil
}
