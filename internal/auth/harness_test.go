package auth

// Test harness: a signing ServiceAccount issuer in miniature — a real RSA
// key, its JWKS document, and minted tokens. Builders hide mechanics, never
// meaning — each test states one clause of the verification contract.

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

const (
	testIssuer   = "https://kubernetes.default.svc.cluster.local"
	testAudience = "llm-gateway"
	agentSubject = "system:serviceaccount:llm:agent-service"
)

// signer is the cluster's token signer in miniature.
type signer struct {
	key *rsa.PrivateKey
	kid string
}

func newSigner(t *testing.T) *signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &signer{key: key, kid: "test-kid-1"}
}

// jwks is the signer's public half as the cluster would publish it.
func (s *signer) jwks() []byte {
	doc := map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"kid": s.kid,
		"n":   base64.RawURLEncoding.EncodeToString(s.key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(s.key.E)).Bytes()),
	}}}
	b, _ := json.Marshal(doc)
	return b
}

// mint signs a token with the standard header; claims override the valid
// defaults, so each test states only the clause it violates.
func (s *signer) mint(t *testing.T, overrides map[string]any) string {
	t.Helper()
	return s.mintWithHeader(t, map[string]any{"alg": "RS256", "kid": s.kid}, overrides)
}

func (s *signer) mintWithHeader(t *testing.T, header, overrides map[string]any) string {
	t.Helper()

	claims := map[string]any{
		"iss": testIssuer,
		"sub": agentSubject,
		"aud": testAudience,
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range overrides {
		claims[k] = v
	}

	seg := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal segment: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}

	signingInput := seg(header) + "." + seg(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// jwksServer publishes the given JWKS document over HTTP.
func jwksServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// warmCache is a KeyCache that has fetched the signer's keys once.
func warmCache(t *testing.T, s *signer) *KeyCache {
	t.Helper()
	cache := coldCache(t, jwksServer(t, s.jwks()).URL)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return cache
}

func coldCache(t *testing.T, url string) *KeyCache {
	t.Helper()
	cache, err := NewKeyCache(url, nil)
	if err != nil {
		t.Fatalf("NewKeyCache: %v", err)
	}
	return cache
}

// agentApp is the app the agent-service subject maps to.
func agentApp() gateway.App {
	return gateway.App{Name: "agent-service"}
}

// directory binds the standard issuer, audience, and app table to the cache.
func directory(t *testing.T, cache *KeyCache) *Directory {
	t.Helper()
	d, err := NewDirectory(Config{
		Issuer:   testIssuer,
		Audience: testAudience,
		Apps:     map[string]gateway.App{agentSubject: agentApp()},
	}, cache)
	if err != nil {
		t.Fatalf("NewDirectory: %v", err)
	}
	return d
}
