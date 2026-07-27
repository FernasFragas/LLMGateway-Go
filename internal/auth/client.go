package auth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DefaultServiceAccountDir is where the kubelet projects a pod's bound
// ServiceAccount token and the cluster CA. NewFetchClient reads its two files;
// the directory's absence means the process is not running in a pod.
const DefaultServiceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

const (
	caFileName    = "ca.crt"
	tokenFileName = "token"
)

// NewFetchClient builds the http.Client the JWKSCache uses to reach the
// in-cluster JWKS endpoint, solving the two failures a default client hits
// against kube-apiserver: trust and authorization.
//
// Both materials already live in every pod under dir, so the fix needs no new
// config keys. Two paths, selected by the environment rather than a flag:
//
//   - dir absent — not in a pod: local dev via `kubectl proxy` reaches an
//     unauthenticated, plaintext endpoint, so a plain default client is
//     exactly right and this stays unconfigured;
//   - dir present — trust the API server against ca.crt and carry the pod's
//     own SA token, because the JWKS endpoint is RBAC-gated.
//
// A present-but-unreadable CA is a boot failure, not a fallback: a pod that
// cannot trust its own control plane must not pretend it can.
func NewFetchClient(dir string) (*http.Client, error) {
	if dir == "" {
		dir = DefaultServiceAccountDir
	}
	caPath := filepath.Join(dir, caFileName)

	if _, err := os.Stat(caPath); errors.Is(err, os.ErrNotExist) {
		return &http.Client{Timeout: defaultFetchTimeout}, nil
	}

	ca, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("auth: read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("auth: cluster CA %q holds no usable certificate", caPath)
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}

	return &http.Client{
		Timeout:   defaultFetchTimeout,
		Transport: &bearerTransport{base: transport, tokenPath: filepath.Join(dir, tokenFileName)},
	}, nil
}

// bearerTransport attaches the pod's ServiceAccount token to every request,
// reading the file each time. The kubelet rotates that token (~hourly); a
// value cached at boot eventually sends an expired credential — the exact
// stale-secret bug this path exists to kill. Do not cache what the platform
// rotates.
type bearerTransport struct {
	base      http.RoundTripper
	tokenPath string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := os.ReadFile(t.tokenPath)
	if err != nil {
		return nil, fmt.Errorf("auth: read ServiceAccount token: %w", err)
	}

	// A RoundTripper must not mutate the caller's request; clone before setting
	// the header.
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))

	return t.base.RoundTrip(clone)
}
