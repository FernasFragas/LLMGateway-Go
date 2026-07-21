package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/FernasFragas/LLMGateway-Go/internal/api"
	"github.com/FernasFragas/LLMGateway-Go/internal/auth"
	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

// Config is everything main wires, each layer's terms already in its own
// vocabulary.
type Config struct {
	Server  api.Config
	Gateway gateway.Config
	Auth    auth.Config
	JWKS    JWKS

	// Limits are each app's three declared currencies, by app name — read by
	// the RateLimiter and SlotLimiter adapters when they land.
	Limits map[string]AppLimits
	// GlobalMaxInFlight caps slots across every app, protecting process
	// memory; each app's max_in_flight must fit under it.
	GlobalMaxInFlight int

	Redis        Redis
	SecretSource SecretSource
	Telemetry    Telemetry
}

// JWKS is the key-refresh loop's tuning — consumed by main, not by auth.
type JWKS struct {
	URL             string
	RefreshInterval time.Duration
}

// AppLimits are one app's declared currencies: request rate, token rate,
// and in-flight slots.
type AppLimits struct {
	RPS             int
	TokensPerMinute int
	MaxInFlight     int
}

// Redis locates the quota and breaker state store (fail open when down).
type Redis struct {
	Addr string
}

// SecretSource locates the provider keys — app identity is the
// ServiceAccount token and needs no secret here.
type SecretSource struct {
	Kind            string
	Path            string
	RefreshInterval time.Duration
}

// Telemetry is the observability plumbing's addresses.
type Telemetry struct {
	OTLPEndpoint  string
	MetricsListen string
}

// defaultRefreshInterval keeps a warm key cache honest without hammering the
// issuer; projected tokens live for hours, key rotations for months.
const defaultRefreshInterval = 5 * time.Minute

// Load reads and strictly parses the gateway config file.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true) // a typo fails the boot, never reinterprets it

	var w wire
	if err := dec.Decode(&w); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}

	cfg, err := w.toDomain()
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}

	return cfg, nil
}

// ─── file → layers ──────────────────────────────────────────────────────

// wire mirrors the file, field for field (see config.example.yaml — the
// executable schema).
type wire struct {
	Server        serverWire         `yaml:"server"`
	Auth          authWire           `yaml:"auth"`
	Routes        []routeWire        `yaml:"routes"`
	FailoverOrder []string           `yaml:"failover_order"`
	Apps          map[string]appWire `yaml:"apps"`
	Redis         redisWire          `yaml:"redis"`
	SecretSource  secretSourceWire   `yaml:"secret_source"`
	Telemetry     telemetryWire      `yaml:"telemetry"`
}

type serverWire struct {
	Listen            string   `yaml:"listen"`
	MaxBodyBytes      int64    `yaml:"max_body_bytes"`
	GlobalMaxInFlight int      `yaml:"global_max_in_flight"`
	TotalDeadline     duration `yaml:"total_deadline"`
	PerTryDeadline    duration `yaml:"per_try_deadline"`
	// The HTTP timeouts are tunable but absent from the example on purpose:
	// api.New's defaults serve until an operator has a reason.
	ReadHeaderTimeout duration `yaml:"read_header_timeout"`
	ReadTimeout       duration `yaml:"read_timeout"`
	WriteTimeout      duration `yaml:"write_timeout"`
	IdleTimeout       duration `yaml:"idle_timeout"`
}

type authWire struct {
	Issuer          string   `yaml:"issuer"`
	Audience        string   `yaml:"audience"`
	JWKSURL         string   `yaml:"jwks_url"`
	RefreshInterval duration `yaml:"refresh_interval"`
}

type routeWire struct {
	Model    string `yaml:"model"`
	Provider string `yaml:"provider"`
	Endpoint string `yaml:"endpoint"`
}

type appWire struct {
	Subject        string     `yaml:"subject"`
	TotalDeadline  duration   `yaml:"total_deadline"`
	Limits         limitsWire `yaml:"limits"`
	FailoverPolicy policyWire `yaml:"failover_policy"`
}

type limitsWire struct {
	RPS             int `yaml:"rps"`
	TokensPerMinute int `yaml:"tokens_per_minute"`
	MaxInFlight     int `yaml:"max_in_flight"`
}

type redisWire struct {
	Addr string `yaml:"addr"`
}

type secretSourceWire struct {
	Kind            string   `yaml:"kind"`
	Path            string   `yaml:"path"`
	RefreshInterval duration `yaml:"refresh_interval"`
}

type telemetryWire struct {
	OTLPEndpoint  string `yaml:"otlp_endpoint"`
	MetricsListen string `yaml:"metrics_listen"`
}

func (w wire) toDomain() (Config, error) {
	interval := time.Duration(w.Auth.RefreshInterval)
	switch {
	case interval == 0:
		interval = defaultRefreshInterval
	case interval < 0:
		return Config{}, errors.New("auth.refresh_interval must be positive")
	}

	providers := make([]gateway.ModelProvider, 0, len(w.Routes))
	routedModels := make(map[string]bool, len(w.Routes))
	for i, r := range w.Routes {
		if r.Model == "" || r.Provider == "" || r.Endpoint == "" {
			return Config{}, fmt.Errorf("routes[%d]: model, provider, and endpoint are all required", i)
		}
		providers = append(providers, gateway.ModelProvider{Model: r.Model, Provider: r.Provider, Endpoint: r.Endpoint})
		routedModels[r.Model] = true
	}

	switch w.SecretSource.Kind {
	case "", "file", "vault":
	default:
		return Config{}, fmt.Errorf("secret_source.kind %q is not file or vault", w.SecretSource.Kind)
	}

	// Map iteration is random; sorted names keep boot errors deterministic.
	names := make([]string, 0, len(w.Apps))
	for name := range w.Apps {
		names = append(names, name)
	}
	sort.Strings(names)

	apps := make(map[string]gateway.App, len(w.Apps))
	limits := make(map[string]AppLimits, len(w.Apps))
	subjects := make(map[string]string, len(w.Apps))
	for _, name := range names {
		a := w.Apps[name]
		if a.Subject == "" {
			return Config{}, fmt.Errorf("app %q: subject is required — identity is the ServiceAccount token", name)
		}
		if owner, dup := subjects[a.Subject]; dup {
			return Config{}, fmt.Errorf("app %q: subject %q already belongs to %q", name, a.Subject, owner)
		}
		if w.Server.GlobalMaxInFlight > 0 && a.Limits.MaxInFlight > w.Server.GlobalMaxInFlight {
			return Config{}, fmt.Errorf("app %q: max_in_flight %d exceeds global_max_in_flight %d", name, a.Limits.MaxInFlight, w.Server.GlobalMaxInFlight)
		}

		policy, err := a.FailoverPolicy.toDomain()
		if err != nil {
			return Config{}, fmt.Errorf("app %q: %w", name, err)
		}
		for _, model := range policy.Allowlist {
			if !routedModels[model] {
				return Config{}, fmt.Errorf("app %q: allowlist names %q but no route serves it", name, model)
			}
		}

		subjects[a.Subject] = name
		apps[a.Subject] = gateway.App{
			Name:          name,
			Policy:        policy,
			TotalDeadline: time.Duration(a.TotalDeadline),
		}
		limits[name] = AppLimits(a.Limits)
	}

	return Config{
		Server: api.Config{
			Addr:              w.Server.Listen,
			ReadHeaderTimeout: time.Duration(w.Server.ReadHeaderTimeout),
			ReadTimeout:       time.Duration(w.Server.ReadTimeout),
			WriteTimeout:      time.Duration(w.Server.WriteTimeout),
			IdleTimeout:       time.Duration(w.Server.IdleTimeout),
			MaxBodyBytes:      w.Server.MaxBodyBytes,
		},
		Gateway: gateway.Config{
			ModelProviders: providers,
			FailoverOrder:  w.FailoverOrder,
			TotalDeadline:  time.Duration(w.Server.TotalDeadline),
			PerTryDeadline: time.Duration(w.Server.PerTryDeadline),
		},
		Auth: auth.Config{
			Issuer:   w.Auth.Issuer,
			Audience: w.Auth.Audience,
			Apps:     apps,
		},
		JWKS:              JWKS{URL: w.Auth.JWKSURL, RefreshInterval: interval},
		Limits:            limits,
		GlobalMaxInFlight: w.Server.GlobalMaxInFlight,
		Redis:             Redis{Addr: w.Redis.Addr},
		SecretSource: SecretSource{
			Kind:            w.SecretSource.Kind,
			Path:            w.SecretSource.Path,
			RefreshInterval: time.Duration(w.SecretSource.RefreshInterval),
		},
		Telemetry: Telemetry{OTLPEndpoint: w.Telemetry.OTLPEndpoint, MetricsListen: w.Telemetry.MetricsListen},
	}, nil
}

// policyWire accepts the file's three policy forms: "any", "same-model", or
// {allowlist: [models...]}. Absent means same-model — substitution is
// opt-in, and a typo is a boot failure, never a silent same-model.
type policyWire struct {
	Kind      string
	Allowlist []string
}

func (p *policyWire) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return policyShapeError()
		}
		if s != string(gateway.PolicyAny) && s != string(gateway.PolicySameModel) {
			return fmt.Errorf("failover_policy %q is not any, same-model, or {allowlist: [models...]}", s)
		}
		p.Kind = s

		return nil
	}

	var m map[string][]string
	if err := value.Decode(&m); err != nil {
		return policyShapeError()
	}
	list, ok := m["allowlist"]
	if !ok || len(m) != 1 {
		return policyShapeError()
	}
	if len(list) == 0 {
		return errors.New("failover_policy allowlist names no substitutes")
	}

	p.Kind = string(gateway.PolicyAllowlist)
	p.Allowlist = list

	return nil
}

func policyShapeError() error {
	return errors.New(`failover_policy is "any", "same-model", or {allowlist: [models...]}`)
}

func (p policyWire) toDomain() (gateway.FailoverPolicy, error) {
	switch kind := gateway.PolicyKind(p.Kind); kind {
	case "":
		return gateway.FailoverPolicy{Kind: gateway.PolicySameModel}, nil
	case gateway.PolicySameModel, gateway.PolicyAny:
		return gateway.FailoverPolicy{Kind: kind}, nil
	case gateway.PolicyAllowlist:
		return gateway.FailoverPolicy{Kind: kind, Allowlist: p.Allowlist}, nil
	default:
		// UnmarshalYAML admits no other kind; reaching here is a config bug.
		return gateway.FailoverPolicy{}, fmt.Errorf("policy kind %q is not one of same-model, allowlist, any", p.Kind)
	}
}

// duration accepts Go duration strings ("90s", "2m") — a bare number would
// hide its unit.
type duration time.Duration

func (d *duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return errors.New(`durations are strings like "90s" or "2m"`)
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration %q: %w", s, err)
	}
	*d = duration(parsed)

	return nil
}
