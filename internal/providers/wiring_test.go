package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
	"github.com/FernasFragas/LLMGateway-Go/internal/keys-provider"
	"github.com/FernasFragas/LLMGateway-Go/internal/openai"
)

// The three pieces main composes — key cache, dialect adapter, router — held
// to the contract they only have together. Each is unit-tested alone; what
// these prove is that a credential travels from a mounted file to the wire,
// and that a refusal comes back around to the cache that can fix it.

func TestTheMountedCredentialReachesTheProvider(t *testing.T) {
	var sent string
	upstream := stubProvider(t, http.StatusOK, &sent)
	path := secretFile(t, "sk-from-the-mount\n")

	r := wired(t, path, upstream)

	if _, err := r.Complete(context.Background(), openaiRoute(upstream), request()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if sent != "Bearer sk-from-the-mount" {
		t.Errorf("Authorization = %q, want the credential the file holds, trimmed", sent)
	}
}

func TestARotatedFileReachesTheWireWithoutRebuildingAnything(t *testing.T) {
	// The adapter is constructed once, at boot. Rotation arrives through the
	// accessor it holds — nothing above it is rebuilt, and no restart happens.
	var sent string
	upstream := stubProvider(t, http.StatusOK, &sent)
	path := secretFile(t, "sk-first")

	r, keys := wiredWithCache(t, path, upstream)

	if _, err := r.Complete(context.Background(), openaiRoute(upstream), request()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if sent != "Bearer sk-first" {
		t.Fatalf("Authorization = %q, want the original credential", sent)
	}

	if err := os.WriteFile(path, []byte("sk-rotated"), 0o600); err != nil {
		t.Fatalf("rotate the mount: %v", err)
	}
	if err := keys.Refresh(context.Background(), "openai"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if _, err := r.Complete(context.Background(), openaiRoute(upstream), request()); err != nil {
		t.Fatalf("Complete after rotation: %v", err)
	}
	if sent != "Bearer sk-rotated" {
		t.Errorf("Authorization = %q, want the rotated credential on the very next request", sent)
	}
}

func TestAProviders401ReloadsTheCredentialOutOfBand(t *testing.T) {
	// Decision #8 end to end: the upstream refuses, the caller gets a
	// provider fault to fail over from — never a 401 of their own — and the
	// key that was wrong on disk is corrected without a restart.
	var sent string
	upstream := stubProvider(t, http.StatusUnauthorized, &sent)
	path := secretFile(t, "sk-revoked")

	r, keys := wiredWithCache(t, path, upstream)

	// The credential rotates at the source while the pod holds the old one —
	// the situation the trigger exists for. Nothing tells the gateway; it
	// finds out when the provider refuses what it is still sending.
	if err := os.WriteFile(path, []byte("sk-repaired"), 0o600); err != nil {
		t.Fatalf("rotate the source: %v", err)
	}
	if got := keys.KeyFor("openai"); got != "sk-revoked" {
		t.Fatalf("cached credential = %q, want the stale one still in force", got)
	}

	_, err := r.Complete(context.Background(), openaiRoute(upstream), request())
	if fault := faultOf(t, err); fault.Kind != gateway.FaultRejected {
		t.Fatalf("fault kind = %q, want rejected — the one fault a refresh can fix", fault.Kind)
	}

	deadline := time.Now().Add(2 * time.Second)
	for keys.KeyFor("openai") != "sk-repaired" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := keys.KeyFor("openai"); got != "sk-repaired" {
		t.Errorf("cached credential = %q, want the repaired one — the rejection never scheduled its reload", got)
	}
}

// ─── harness ────────────────────────────────────────────────────────────────

func stubProvider(t *testing.T, status int, sentAuth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*sentAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"},` +
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func secretFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openai")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	return path
}

// wiredWithCache composes exactly what main composes: a file-backed cache, the
// real OpenAI adapter holding an accessor into it, and the router reporting
// rejections back to the cache.
func wiredWithCache(t *testing.T, secretPath string, _ *httptest.Server) (*Router, *keys_provider.Cache) {
	t.Helper()
	keys, err := keys_provider.New(keys_provider.NewFile(), map[string]keys_provider.Source{
		"openai": {Path: secretPath, RefreshInterval: time.Hour},
	})
	if err != nil {
		t.Fatalf("keys-provider.New: %v", err)
	}
	if err := keys.RefreshAll(context.Background()); err != nil {
		t.Fatalf("first load: %v", err)
	}

	r, err := NewRouter(
		map[string]gateway.ProviderClient{"openai": openai.NewOpenAI(nil, keys.Key("openai"))},
		func(provider string) { keys.TriggerRefresh(provider) },
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	return r, keys
}

func wired(t *testing.T, secretPath string, upstream *httptest.Server) *Router {
	t.Helper()
	r, _ := wiredWithCache(t, secretPath, upstream)

	return r
}

func openaiRoute(srv *httptest.Server) gateway.ModelProvider {
	return gateway.ModelProvider{Model: "gpt-4.1", Provider: "openai", Endpoint: srv.URL}
}

func request() gateway.ChatRequest {
	return gateway.ChatRequest{
		MaxTokens: 16,
		Messages:  []gateway.Message{{Role: gateway.RoleUser, Content: "ping"}},
	}
}

func faultOf(t *testing.T, err error) *gateway.ProviderFault {
	t.Helper()
	var fault *gateway.ProviderFault
	if !errors.As(err, &fault) {
		t.Fatalf("error %v is not a *gateway.ProviderFault", err)
	}

	return fault
}
