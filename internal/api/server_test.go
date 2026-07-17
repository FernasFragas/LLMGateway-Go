package api

// The lifecycle contract: Start serves real traffic, Shutdown drains
// gracefully and Start reports it as a clean exit, and New refuses to build
// a server missing its required dependencies.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/health"
)

func TestNewRefusesMissingDeps(t *testing.T) {
	if _, err := New(Config{}, Deps{Health: health.NewChecker()}); err == nil {
		t.Error("New must refuse a nil ChatService")
	}
	if _, err := New(Config{}, Deps{Chat: &chatStub{}}); err == nil {
		t.Error("New must refuse a nil health.Checker")
	}
}

func TestStartServesAndShutdownDrainsCleanly(t *testing.T) {
	srv, err := New(Config{Addr: "127.0.0.1:0"}, Deps{Chat: &chatStub{}, Health: health.NewChecker()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	started := make(chan error, 1)
	go func() { started <- srv.Start() }()

	addr := waitForAddr(t, srv)
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz over the socket: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz over the socket = %d, want 200", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case err := <-started:
		if err != nil {
			t.Errorf("Start after graceful shutdown = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Shutdown")
	}
}

func waitForAddr(t *testing.T, srv *Server) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if addr := srv.Addr(); addr != "" {
			return addr
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server never bound an address")
	return ""
}
