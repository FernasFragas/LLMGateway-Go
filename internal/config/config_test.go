package config

import (
	"os"
	"testing"
	"time"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestExampleFileIsTheExecutableSchema(t *testing.T) {
	cfg, err := Load(exampleFile)
	if err != nil {
		t.Fatalf("the shipped example must always load: %v", err)
	}

	if cfg.Server.Addr != ":8080" || cfg.Server.MaxBodyBytes != 524288 {
		t.Errorf("Server = %+v, want the example's transport tuning", cfg.Server)
	}
	if cfg.Gateway.TotalDeadline != 30*time.Second || cfg.Gateway.PerTryDeadline != 10*time.Second {
		t.Errorf("Gateway budgets = %+v, want the example's", cfg.Gateway)
	}
	if len(cfg.Gateway.ModelProviders) != 3 || cfg.Gateway.ModelProviders[0].Model != "gpt-4.1" {
		t.Errorf("ModelProviders = %+v, want the example's three routes in order", cfg.Gateway.ModelProviders)
	}
	if len(cfg.Gateway.FailoverOrder) != 3 || cfg.Gateway.FailoverOrder[0] != "openai" {
		t.Errorf("FailoverOrder = %v, want the example's", cfg.Gateway.FailoverOrder)
	}
	if cfg.Auth.Audience != "llm-gateway" || cfg.Auth.Issuer == "" {
		t.Errorf("Auth = %+v, want the example's identity terms", cfg.Auth)
	}
	if cfg.JWKS.URL == "" || cfg.JWKS.RefreshInterval != 5*time.Minute {
		t.Errorf("JWKS = %+v, want the example's refresh tuning", cfg.JWKS)
	}

	agent := cfg.Auth.Apps["system:serviceaccount:llm:agent-service"]
	if agent.Name != "agent-service" || agent.Policy.Kind != gateway.PolicySameModel || agent.TotalDeadline != 150*time.Second {
		t.Errorf("agent-service = %+v, want its declared terms", agent)
	}
	bot := cfg.Auth.Apps["system:serviceaccount:llm:support-bot"]
	if bot.Policy.Kind != gateway.PolicyAllowlist || len(bot.Policy.Allowlist) != 2 {
		t.Errorf("support-bot policy = %+v, want its two named substitutes", bot.Policy)
	}
	if rag := cfg.Auth.Apps["system:serviceaccount:llm:rag-api"]; rag.Policy.Kind != gateway.PolicyAny {
		t.Errorf("rag-api policy = %+v, want any", rag.Policy)
	}

	if cfg.Limits["agent-service"] != (AppLimits{RPS: 5, TokensPerMinute: 500000, MaxInFlight: 300}) {
		t.Errorf("agent-service limits = %+v, want the example's three currencies", cfg.Limits["agent-service"])
	}
	if cfg.GlobalMaxInFlight != 800 {
		t.Errorf("GlobalMaxInFlight = %d, want 800", cfg.GlobalMaxInFlight)
	}
	if cfg.Redis.Addr == "" || cfg.SecretSource.Kind != "vault" || cfg.Telemetry.MetricsListen != ":9090" {
		t.Errorf("infra sections = %+v %+v %+v, want the example's", cfg.Redis, cfg.SecretSource, cfg.Telemetry)
	}

	if cfg.SecretSource.Vault.Role == "" || cfg.SecretSource.Vault.AuthPath != "auth/kubernetes" {
		t.Errorf("Vault = %+v, want the example's Kubernetes auth terms", cfg.SecretSource.Vault)
	}
	// Ollama is absent on purpose: the in-cluster route needs no credential.
	if _, keyed := cfg.SecretSource.Providers["ollama"]; keyed || len(cfg.SecretSource.Providers) != 2 {
		t.Errorf("Providers = %+v, want only the two that need a key", cfg.SecretSource.Providers)
	}
	if got := cfg.SecretSource.Providers["openai"].RefreshInterval; got != time.Minute {
		t.Errorf("openai cadence = %v, want its own 1m override, not the block default", got)
	}
	if got := cfg.SecretSource.Providers["anthropic"].RefreshInterval; got != 5*time.Minute {
		t.Errorf("anthropic cadence = %v, want the block's 5m inherited", got)
	}
}

func TestProviderSecretMustNameARoutedProvider(t *testing.T) {
	// Absence is meaningful here — no entry means no credential — so a typo
	// and a deliberately keyless provider are indistinguishable once dropped.
	// The file refuses rather than leaving it to surface as upstream 401s.
	wantBootFailure(t, `
routes:
  - {model: gpt-4.1, provider: openai, endpoint: "https://api.openai.com/v1"}
secret_source:
  kind: file
  providers: {opanai: {path: ./keys-provider/openai}}
`, "no route names that provider")
}

func TestVaultBlockWithoutVaultKindIsRefused(t *testing.T) {
	wantBootFailure(t, `
secret_source:
  kind: file
  vault: {address: "https://vault:8200", role: llm-gateway}
`, "would never be read")
}

func TestVaultKindDemandsItsAddressAndRole(t *testing.T) {
	wantBootFailure(t, "secret_source:\n  kind: vault", "address and role are required")
}

func TestProviderSecretDemandsAPath(t *testing.T) {
	wantBootFailure(t, `
routes:
  - {model: gpt-4.1, provider: openai, endpoint: "https://api.openai.com/v1"}
secret_source:
  kind: file
  providers: {openai: {refresh_interval: 1m}}
`, "path is required")
}

func TestProvidersWithoutAKindAreRefused(t *testing.T) {
	wantBootFailure(t, `
routes:
  - {model: gpt-4.1, provider: openai, endpoint: "https://api.openai.com/v1"}
secret_source:
  providers: {openai: {path: ./keys-provider/openai}}
`, "kind is required")
}

func TestNoSecretSourceConfiguredIsValid(t *testing.T) {
	// Every route self-hosted: having nothing to fetch is a steady state.
	cfg, err := Load(write(t, `
routes:
  - {model: llama3, provider: ollama, endpoint: "http://localhost:11434"}
`))
	if err != nil {
		t.Fatalf("a gateway whose routes need no credentials must load: %v", err)
	}
	if len(cfg.SecretSource.Providers) != 0 {
		t.Errorf("Providers = %+v, want none", cfg.SecretSource.Providers)
	}
}

func TestOmittedProviderCadenceInheritsTheBlockDefault(t *testing.T) {
	cfg, err := Load(write(t, `
routes:
  - {model: gpt-4.1, provider: openai, endpoint: "https://api.openai.com/v1"}
secret_source:
  kind: file
  refresh_interval: 90s
  providers: {openai: {path: ./keys-provider/openai}}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.SecretSource.Providers["openai"].RefreshInterval; got != 90*time.Second {
		t.Errorf("cadence = %v, want the block's 90s — an omitted interval inherits, never zero", got)
	}
}

func TestLocalOperatorFileStillParsesWhenPresent(t *testing.T) {
	// config/config.yaml is the operator's, gitignored — but its header
	// promises validation whenever it exists, so schema changes can't
	// silently strand a local setup.
	const local = "../../config/config.yaml"
	if _, err := os.Stat(local); os.IsNotExist(err) {
		t.Skip("no local operator file — nothing to hold to the schema")
	}
	if _, err := Load(local); err != nil {
		t.Errorf("the local operator file no longer parses — the schema moved without it: %v", err)
	}
}

func TestUnknownKeyFailsTheBoot(t *testing.T) {
	wantBootFailure(t, "server:\n  adress: \":8080\"", "adress")
}

func TestTypoedPolicyIsRejectedNotReinterpreted(t *testing.T) {
	// The core reads an unknown kind as same-model — conservative at
	// runtime, a silent policy change at boot. The file refuses it.
	wantBootFailure(t, `apps: {a: {subject: s, failover_policy: alowlist}}`, "alowlist")
	wantBootFailure(t, `apps: {a: {subject: s, failover_policy: {alowlist: [m]}}}`, "allowlist")
}

func TestOmittedPolicyIsSameModel(t *testing.T) {
	cfg, err := Load(write(t, `apps: {a: {subject: s}}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Apps["s"].Policy.Kind != gateway.PolicySameModel {
		t.Error("an app declaring no policy must get same-model — substitution is opt-in")
	}
}

func TestEmptyAllowlistIsRejected(t *testing.T) {
	wantBootFailure(t, `apps: {a: {subject: s, failover_policy: {allowlist: []}}}`, "names no substitutes")
}

func TestAllowlistEntriesMustBeRouted(t *testing.T) {
	wantBootFailure(t, `
routes:
  - {model: gpt-4.1, provider: openai, endpoint: "https://api.openai.com/v1"}
apps:
  a: {subject: s, failover_policy: {allowlist: [claude-sonnet-4]}}
`, "no route serves it")
}

func TestDuplicateSubjectIsRejected(t *testing.T) {
	wantBootFailure(t, `apps: {a: {subject: s}, b: {subject: s}}`, "already belongs to")
}

func TestMissingSubjectIsRejected(t *testing.T) {
	wantBootFailure(t, `apps: {a: {}}`, "subject is required")
}

func TestAppCannotExceedTheGlobalCeiling(t *testing.T) {
	wantBootFailure(t, `
server: {global_max_in_flight: 100}
apps:
  a: {subject: s, limits: {max_in_flight: 200}}
`, "exceeds global_max_in_flight")
}

func TestIncompleteRouteIsRejected(t *testing.T) {
	wantBootFailure(t, `routes: [{model: gpt-4.1, provider: openai}]`, "routes[0]")
}

func TestBadDurationNamesItself(t *testing.T) {
	wantBootFailure(t, `server: {total_deadline: "ninety seconds"}`, "ninety seconds")
	wantBootFailure(t, `server: {total_deadline: 90}`, "missing unit")
}

func TestOmittedTuningTakesConsumerDefaults(t *testing.T) {
	cfg, err := Load(write(t, `{}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Addr != "" || cfg.Server.WriteTimeout != 0 {
		t.Errorf("Server = %+v, want zero values — api.New owns the defaults", cfg.Server)
	}
	if cfg.JWKS.RefreshInterval != defaultRefreshInterval {
		t.Errorf("RefreshInterval = %v, want the %v default", cfg.JWKS.RefreshInterval, defaultRefreshInterval)
	}
}

func TestMissingFileIsAnError(t *testing.T) {
	if _, err := Load("/nowhere/gateway.yaml"); err == nil {
		t.Error("Load must report a missing file, not invent a config")
	}
}
