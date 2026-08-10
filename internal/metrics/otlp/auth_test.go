package otlp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/auth"
)

func TestJWKSAgeIsAbsentUntilTheKeysFirstLoad(t *testing.T) {
	// The unguarded version reports ~9.2e9 seconds here, measured from the
	// zero time — enough to page someone about a pod that is simply
	// starting. Readiness already gates a cold pod; this gauge is for the
	// warm-but-stalling case, and absence is how it stays that way.
	mp, reader := meterAndReader()
	cache := jwksCache(t, jwksEndpoint(t))

	if err := RegisterAuth(mp.Meter("test"), cache); err != nil {
		t.Fatalf("RegisterAuth: %v", err)
	}

	if values, present := collectedFloat(t, reader)["jwks_cache_age_seconds"]; present {
		t.Errorf("jwks_cache_age_seconds = %v before any load, want no data point at all", values)
	}
}

func TestJWKSAgeIsReportedOnceTheKeysLoad(t *testing.T) {
	mp, reader := meterAndReader()
	cache := jwksCache(t, jwksEndpoint(t))
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if err := RegisterAuth(mp.Meter("test"), cache); err != nil {
		t.Fatalf("RegisterAuth: %v", err)
	}

	values, present := collectedFloat(t, reader)["jwks_cache_age_seconds"]
	if !present || len(values) != 1 {
		t.Fatalf("jwks_cache_age_seconds = %v, want exactly one data point", values)
	}
	if values[0] < 0 {
		t.Errorf("age = %v, want a non-negative number of seconds", values[0])
	}
}

// jwksCache builds a cache pointed at url, unloaded.
func jwksCache(t *testing.T, url string) *auth.JWKSCache {
	t.Helper()
	cache, err := auth.NewJWKSCache(url, nil)
	if err != nil {
		t.Fatalf("NewJWKSCache: %v", err)
	}

	return cache
}

// jwksEndpoint serves one RSA key in JWKS form — enough for Refresh to
// succeed, which is all these tests need from it.
func jwksEndpoint(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	doc := map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"kid": "test-key",
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal JWKS: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(body); err != nil {
			t.Errorf("write JWKS: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	return fmt.Sprintf("%s/openid/v1/jwks", srv.URL)
}
