package main

// The composition contract: one request through newServer's full chain must
// land in every layer it promises — the log, the counters, and the wire —
// with no package knowing the others exist.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/api"
	"github.com/FernasFragas/LLMGateway-Go/internal/auth"
	"github.com/FernasFragas/LLMGateway-Go/internal/config"
	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/health"
	"github.com/FernasFragas/LLMGateway-Go/internal/metrics/api"
)

func TestOnePanickedRequestYieldsTheLogLineTheCountsAndThe500(t *testing.T) {
	srv, out, reqs, panics := composed(t, panickingCore{})

	rec := postChat(srv, "sk-test-key")

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal_error") {
		t.Errorf("caller got %d %q, want the standard 500 ErrorBody", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "the panic value") {
		t.Error("the panic value must never reach the caller")
	}
	for _, want := range []string{"handler panicked", "the panic value", "stack=", "request served", "status=500"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("log does not mention %q:\n%s", want, out.String())
		}
	}
	if panics.Panics() != 1 || reqs.Requests() != 1 || reqs.ServerErrors() != 1 {
		t.Errorf("counted panics=%d requests=%d 5xx=%d, want 1/1/1",
			panics.Panics(), reqs.Requests(), reqs.ServerErrors())
	}
}

func TestACalmRequestIsLoggedCountedAndServed(t *testing.T) {
	srv, out, reqs, panics := composed(t, servingCore{})

	rec := postChat(srv, "sk-test-key")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"request served", "POST", "/v1/chat", "status=200"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("log does not mention %q:\n%s", want, out.String())
		}
	}
	if reqs.Requests() != 1 || reqs.ServerErrors() != 0 || panics.Panics() != 0 {
		t.Errorf("counted requests=%d 5xx=%d panics=%d, want 1/0/0",
			reqs.Requests(), reqs.ServerErrors(), panics.Panics())
	}
}

// composed builds the server exactly as main will, with the log captured
// and the counters exposed.
func composed(t *testing.T, core api.ChatService) (*api.Server, *bytes.Buffer, *metrics.RequestMetrics, *metrics.PanicCounter) {
	t.Helper()
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reqs, panics := metrics.NewRequestMetrics(), metrics.NewPanicCounter()

	srv, err := newServer(api.Config{}, core, health.NewChecker(), log, reqs, panics)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return srv, buf, reqs, panics
}

// postChat sends a schema-valid chat request carrying the given credential
// through the full chain.
func postChat(srv *api.Server, token string) *httptest.ResponseRecorder {
	body := `{"model":"gpt-4.1","max_tokens":512,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestMintedTokenReachesTheModelStrangersDoNot is the identity chain end to
// end: HTTP edge → bearer extraction → real core → local JWT verification →
// app terms — composed exactly as main will compose it.
func TestMintedTokenReachesTheModelStrangersDoNot(t *testing.T) {
	key := mustRSAKey(t)
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(jwksFor(key, "kid-1"))
	}))
	t.Cleanup(issuer.Close)

	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	checker := health.NewChecker()
	dir, keys, err := newAppDirectory(config.Config{
		Auth: auth.Config{
			Issuer:   tokenIssuer,
			Audience: "llm-gateway",
			Apps:     map[string]gateway.App{agentSubject: {Name: "agent-service"}},
		},
		JWKS: config.JWKS{URL: issuer.URL, RefreshInterval: time.Minute},
	}, checker, log)
	if err != nil {
		t.Fatalf("newAppDirectory: %v", err)
	}

	if checker.Ready(context.Background()) == nil {
		t.Error("a pod with no keys must refuse readiness — fail closed where no traffic is harmed")
	}
	if err := keys.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := checker.Ready(context.Background()); err != nil {
		t.Errorf("Ready = %v, want nil once the issuer answered", err)
	}

	core, err := gateway.New(gateway.Config{
		ModelProviders: []gateway.ModelProvider{{Model: "gpt-4.1", Provider: "openai", Endpoint: "http://fake"}},
		TotalDeadline:  5 * time.Second,
		PerTryDeadline: time.Second,
	}, gateway.Deps{Apps: dir, RateLimiter: openGate{}, Slots: freeSlots{}, Provider: answering{}})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}

	srv, err := newServer(api.Config{}, core, checker, log, metrics.NewRequestMetrics(), metrics.NewPanicCounter())
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}

	rec := postChat(srv, mintToken(t, key, "kid-1", map[string]any{}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "an answer") {
		t.Errorf("minted token got %d %q, want the model's answer", rec.Code, rec.Body.String())
	}

	rec = postChat(srv, "sk-some-stale-api-key")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "unauthorized") {
		t.Errorf("a stranger's credential got %d %q, want 401 unauthorized", rec.Code, rec.Body.String())
	}

	rec = postChat(srv, mintToken(t, key, "kid-1", map[string]any{"aud": "another-service"}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a token bound to another audience got %d, want 401 — bound means bound", rec.Code)
	}
}

const (
	tokenIssuer  = "https://kubernetes.default.svc.cluster.local"
	agentSubject = "system:serviceaccount:llm:agent-service"
)

func mustRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// jwksFor is the signer's public half as the cluster would publish it.
func jwksFor(key *rsa.PrivateKey, kid string) []byte {
	doc := map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}}
	b, _ := json.Marshal(doc)
	return b
}

// mintToken signs a projected-token stand-in; overrides violate one clause.
func mintToken(t *testing.T, key *rsa.PrivateKey, kid string, overrides map[string]any) string {
	t.Helper()

	claims := map[string]any{
		"iss": tokenIssuer,
		"sub": agentSubject,
		"aud": "llm-gateway",
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

	signingInput := seg(map[string]any{"alg": "RS256", "kid": kid}) + "." + seg(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// openGate, freeSlots, and answering stand in for the limiter, slot, and
// provider adapters still to come — this test is about identity, not limits.
type openGate struct{}

func (openGate) Allow(context.Context, string) (gateway.RateDecision, error) {
	return gateway.RateDecision{Allowed: true}, nil
}

type freeSlots struct{}

func (freeSlots) TryAcquire(string) (func(), int, bool) { return func() {}, 0, true }

type answering struct{}

func (answering) Complete(context.Context, gateway.ModelProvider, gateway.ChatRequest) (gateway.Completion, error) {
	return gateway.Completion{
		Message:      gateway.Message{Role: gateway.RoleAssistant, Content: "an answer"},
		FinishReason: gateway.FinishStop,
		Usage:        gateway.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}, nil
}

// panickingCore stands in for a handler bug.
type panickingCore struct{}

func (panickingCore) Chat(context.Context, string, gateway.ChatRequest) (gateway.ChatResult, error) {
	panic("the panic value")
}

// servingCore answers every chat with a canned completion.
type servingCore struct{}

func (servingCore) Chat(context.Context, string, gateway.ChatRequest) (gateway.ChatResult, error) {
	return gateway.ChatResult{
		Model:        "gpt-4.1",
		Provider:     "openai",
		Message:      gateway.Message{Role: gateway.RoleAssistant, Content: "an answer"},
		FinishReason: gateway.FinishStop,
	}, nil
}
