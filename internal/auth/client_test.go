package auth

// NewFetchClient's contract: outside a pod it stays a plain client so local
// dev works unconfigured; inside one it trusts the mounted CA and carries the
// SA token — freshly read every request, because the kubelet rotates it.

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// serviceAccountDir writes a mount the way the kubelet projects one: the
// server's CA so the client trusts it, and a token to present.
func serviceAccountDir(t *testing.T, cert *x509.Certificate, token string) string {
	t.Helper()
	dir := t.TempDir()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(filepath.Join(dir, caFileName), caPEM, 0o600); err != nil {
		t.Fatalf("write %s: %v", caFileName, err)
	}
	writeToken(t, dir, token)
	return dir
}

func writeToken(t *testing.T, dir, token string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, tokenFileName), []byte(token), 0o600); err != nil {
		t.Fatalf("write %s: %v", tokenFileName, err)
	}
}

// capturingTLSServer records the Authorization header of the last request.
func capturingTLSServer(t *testing.T, gotAuth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchClientWithoutMountStaysDefault(t *testing.T) {
	client, err := NewFetchClient(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("NewFetchClient: %v", err)
	}

	if _, ok := client.Transport.(*bearerTransport); ok {
		t.Error("outside a pod the client must attach no bearer token — local dev via kubectl proxy is unauthenticated")
	}
}

func TestFetchClientTrustsMountedCAAndSendsToken(t *testing.T) {
	var gotAuth string
	srv := capturingTLSServer(t, &gotAuth)
	dir := serviceAccountDir(t, srv.Certificate(), "minted-token")

	client, err := NewFetchClient(dir)
	if err != nil {
		t.Fatalf("NewFetchClient: %v", err)
	}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("the client must trust a server signed by the mounted CA: %v", err)
	}
	resp.Body.Close()

	if got, want := gotAuth, "Bearer minted-token"; got != want {
		t.Errorf("Authorization = %q, want %q — the RBAC-gated endpoint needs the pod's SA token", got, want)
	}
}

func TestFetchClientRereadsRotatedToken(t *testing.T) {
	var gotAuth string
	srv := capturingTLSServer(t, &gotAuth)
	dir := serviceAccountDir(t, srv.Certificate(), "first-token")

	client, err := NewFetchClient(dir)
	if err != nil {
		t.Fatalf("NewFetchClient: %v", err)
	}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	resp.Body.Close()

	writeToken(t, dir, "second-token") // the kubelet rotates it under us

	resp, err = client.Get(srv.URL)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	resp.Body.Close()

	if got, want := gotAuth, "Bearer second-token"; got != want {
		t.Errorf("Authorization = %q, want %q — a token read once at boot goes stale; each request must re-read the file", got, want)
	}
}
