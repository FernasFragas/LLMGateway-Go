package keys_provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

// maxResponseBytes caps what a Vault reply may cost in memory. Vault is a
// trusted in-cluster service, but "trusted" is not a reason to hand it
// unbounded memory — a misrouted address answering with something enormous
// should fail, not fill the heap. Same bound the provider adapters use.
const maxResponseBytes = 10 << 20

// secretField is where a provider's credential lives inside its secret. One
// convention beats a per-provider setting nobody would vary (ADR-001).
const secretField = "key"

// Vault reads credentials from HashiCorp Vault, authenticating with the
// Kubernetes auth method: the gateway trades its own projected ServiceAccount
// token for a Vault token, so no credential has to be distributed in order to
// read credentials.
//
// The Vault token is leased and expires, so it is obtained lazily, reused
// across reads, and re-obtained when Vault rejects it — a second lifecycle,
// unrelated to the per-provider cadences above, and the surface the file
// fetcher does not have.
type Vault struct {
	client    *http.Client
	address   string
	role      string
	authPath  string
	tokenPath string // the pod's own projected ServiceAccount token

	mu    sync.Mutex
	token string
}

// VaultConfig is what reaching Vault takes. TokenPath defaults to the
// kubelet's projection point.
type VaultConfig struct {
	Address   string
	Role      string
	AuthPath  string
	TokenPath string
}

// NewVault builds the Vault fetcher; nil client means http.DefaultClient —
// deadlines come from the caller's context, not the client.
func NewVault(client *http.Client, cfg VaultConfig) (*Vault, error) {
	switch {
	case cfg.Address == "":
		return nil, errors.New("keys-provider: vault address is required")
	case cfg.Role == "":
		return nil, errors.New("keys-provider: vault role is required")
	case cfg.AuthPath == "":
		return nil, errors.New("keys-provider: vault auth path is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	if cfg.TokenPath == "" {
		cfg.TokenPath = defaultTokenPath
	}

	return &Vault{
		client:    client,
		address:   strings.TrimSuffix(cfg.Address, "/"),
		role:      cfg.Role,
		authPath:  strings.Trim(cfg.AuthPath, "/"),
		tokenPath: cfg.TokenPath,
	}, nil
}

// defaultTokenPath is where the kubelet projects the pod's own token — the
// same file internal/auth's client re-reads per request.
const defaultTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// Fetch reads path and returns the credential under the `key` field. path is
// the literal API path, `data/` segment included for a KV v2 mount, so a
// wrong mount version fails as a visible 404 rather than a silent rewrite.
//
// A rejected Vault token is retried exactly once, after re-authenticating:
// that is the expected end of a lease, not a failure worth propagating. Any
// second rejection travels, because by then the role or the policy is wrong
// and retrying would only spin.
func (v *Vault) Fetch(ctx context.Context, path string) (string, error) {
	token, err := v.currentToken(ctx)
	if err != nil {
		return "", err
	}

	key, status, err := v.read(ctx, path, token)
	if err == nil {
		return key, nil
	}
	if status != http.StatusForbidden && status != http.StatusUnauthorized {
		return "", err
	}

	token, err = v.reauthenticate(ctx)
	if err != nil {
		return "", err
	}
	key, _, err = v.read(ctx, path, token)

	return key, err
}

// read performs one authenticated GET, reporting the status alongside the
// error so Fetch can tell "the lease ended" from "the path is wrong".
func (v *Vault) read(ctx context.Context, path, token string) (string, int, error) {
	url := v.address + "/v1/" + strings.TrimPrefix(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, fmt.Errorf("build vault read: %w", err)
	}
	req.Header.Set("X-Vault-Token", token)

	resp, err := v.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("vault read %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("vault read %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("vault read %s: status %d", path, resp.StatusCode)
	}

	// KV v2 nests the secret one level deeper than KV v1; accepting both
	// means an operator's mount version is not a code change.
	var body struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", resp.StatusCode, fmt.Errorf("vault read %s: decode: %w", path, err)
	}

	key, ok := body.Data.Data[secretField]
	if !ok {
		return "", resp.StatusCode, fmt.Errorf("vault read %s: secret has no %q field", path, secretField)
	}

	return key, resp.StatusCode, nil
}

// currentToken returns the cached Vault token, obtaining one if this is the
// first read.
func (v *Vault) currentToken(ctx context.Context) (string, error) {
	v.mu.Lock()
	token := v.token
	v.mu.Unlock()

	if token != "" {
		return token, nil
	}

	return v.reauthenticate(ctx)
}

// reauthenticate trades the pod's projected ServiceAccount token for a fresh
// Vault token. The SA token is re-read every time: the kubelet rotates it in
// place, and a copy held from boot would expire.
func (v *Vault) reauthenticate(ctx context.Context) (string, error) {
	jwt, err := os.ReadFile(v.tokenPath)
	if err != nil {
		return "", fmt.Errorf("read serviceaccount token: %w", err)
	}

	body, err := json.Marshal(map[string]string{
		"role": v.role,
		"jwt":  strings.TrimSpace(string(jwt)),
	})
	if err != nil {
		return "", fmt.Errorf("encode vault login: %w", err)
	}

	url := v.address + "/v1/" + v.authPath + "/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build vault login: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("vault login: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("vault login: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vault login: status %d", resp.StatusCode)
	}

	var login struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(raw, &login); err != nil {
		return "", fmt.Errorf("vault login: decode: %w", err)
	}
	if login.Auth.ClientToken == "" {
		return "", errors.New("vault login: response carried no client token")
	}

	v.mu.Lock()
	v.token = login.Auth.ClientToken
	v.mu.Unlock()

	return login.Auth.ClientToken, nil
}
