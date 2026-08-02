package providerkeys

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVaultTradesTheServiceAccountTokenForAVaultToken(t *testing.T) {
	// The Kubernetes auth method is why no credential has to be distributed
	// in order to read credentials: the pod's own projected token is the
	// only thing presented.
	vault := fakeVault(t)
	src := vaultFetcher(t, vault, writeSecret(t, "projected-sa-token"))

	got, err := src.Fetch(context.Background(), "secret/data/llm/openai")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if got != "sk-from-vault" {
		t.Errorf("Fetch = %q, want the secret's key field", got)
	}
	if vault.presentedJWT != "projected-sa-token" || vault.presentedRole != "llm-gateway" {
		t.Errorf("logged in as role %q with jwt %q, want the pod's own token and configured role", vault.presentedRole, vault.presentedJWT)
	}
}

func TestVaultReusesItsTokenAcrossReads(t *testing.T) {
	vault := fakeVault(t)
	src := vaultFetcher(t, vault, writeSecret(t, "projected-sa-token"))

	for range 3 {
		if _, err := src.Fetch(context.Background(), "secret/data/llm/openai"); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	}

	if vault.logins != 1 {
		t.Errorf("logins = %d, want 1 — re-authenticating per read would put a round trip on every refresh", vault.logins)
	}
}

func TestVaultReauthenticatesWhenItsLeaseEnds(t *testing.T) {
	// A leased token expiring is the expected end of its life, not a failure
	// worth surfacing — one silent re-login, then the read proceeds.
	vault := fakeVault(t)
	src := vaultFetcher(t, vault, writeSecret(t, "projected-sa-token"))

	if _, err := src.Fetch(context.Background(), "secret/data/llm/openai"); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	vault.expireToken()

	got, err := src.Fetch(context.Background(), "secret/data/llm/openai")
	if err != nil {
		t.Fatalf("Fetch after the lease ended: %v", err)
	}
	if got != "sk-from-vault" {
		t.Errorf("Fetch = %q, want the credential after a transparent re-login", got)
	}
	if vault.logins != 2 {
		t.Errorf("logins = %d, want exactly one re-login", vault.logins)
	}
}

func TestVaultStopsAfterASecondRejection(t *testing.T) {
	// By the second 403 the role or the policy is wrong; retrying only spins.
	vault := fakeVault(t)
	vault.alwaysForbid = true
	src := vaultFetcher(t, vault, writeSecret(t, "projected-sa-token"))

	if _, err := src.Fetch(context.Background(), "secret/data/llm/openai"); err == nil {
		t.Fatal("a persistently refused read must report, not loop")
	}
	if vault.logins > 2 {
		t.Errorf("logins = %d, want at most one retry", vault.logins)
	}
}

func TestVaultReportsASecretMissingItsKeyField(t *testing.T) {
	vault := fakeVault(t)
	vault.field = "api_key" // not the convention
	src := vaultFetcher(t, vault, writeSecret(t, "projected-sa-token"))

	_, err := src.Fetch(context.Background(), "secret/data/llm/openai")
	if err == nil || !strings.Contains(err.Error(), secretField) {
		t.Errorf("error = %v, want it to name the %q field the convention expects", err, secretField)
	}
}

func TestVaultDemandsItsAddressAndRole(t *testing.T) {
	if _, err := NewVault(nil, VaultConfig{Role: "r", AuthPath: "auth/kubernetes"}); err == nil {
		t.Error("an address is required")
	}
	if _, err := NewVault(nil, VaultConfig{Address: "https://vault:8200", AuthPath: "auth/kubernetes"}); err == nil {
		t.Error("a role is required")
	}
}

// ─── the fake Vault ─────────────────────────────────────────────────────────

type vaultServer struct {
	*httptest.Server
	logins        int
	presentedJWT  string
	presentedRole string
	token         string
	field         string
	alwaysForbid  bool
}

func (v *vaultServer) expireToken() { v.token = "" }

// fakeVault answers the two endpoints the fetcher uses: the Kubernetes auth
// login and a KV v2 read.
func fakeVault(t *testing.T) *vaultServer {
	t.Helper()
	v := &vaultServer{field: secretField}

	v.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/login"):
			var body struct{ Role, Jwt string }
			_ = decodeJSON(r, &body)
			v.logins++
			v.presentedRole, v.presentedJWT = body.Role, body.Jwt
			v.token = "vault-token"
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"auth":{"client_token":"vault-token"}}`))

		default:
			if v.alwaysForbid || v.token == "" || r.Header.Get("X-Vault-Token") != v.token {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"data":{"` + v.field + `":"sk-from-vault"}}}`))
		}
	}))
	t.Cleanup(v.Close)

	return v
}

func vaultFetcher(t *testing.T, vault *vaultServer, tokenPath string) *Vault {
	t.Helper()
	src, err := NewVault(nil, VaultConfig{
		Address:   vault.URL,
		Role:      "llm-gateway",
		AuthPath:  "auth/kubernetes",
		TokenPath: tokenPath,
	})
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	return src
}
