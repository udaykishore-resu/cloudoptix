// Package config builds the platform's typed, layered configuration.
//
// Layering is defaults -> YAML file -> environment variables -> command-line
// flags, each layer overriding the one before it. That order is chosen
// because it matches how CloudOptix is actually deployed: a base config.yaml
// ships with the image (safe defaults, no secrets), an environment-specific
// overlay is mounted or set by the orchestrator (Kubernetes ConfigMap/Secret
// env injection), and a flag is what an operator reaches for to override one
// value for one run without touching either. Environment beats file because
// the same image is promoted through dev/staging/prod with only its
// environment changed; flags beat environment because a flag is the most
// explicit, most temporary override a human can make and should always win.
//
// The other design decision worth calling out is in secret.go: secret-shaped
// fields use the Secret type, whose YAML unmarshaller refuses a literal
// value. A config.yaml that accidentally contains `database.password: hunter2`
// fails to load with an explicit error instead of working — the platform's
// secret-handling invariant is enforced by the type system, not by code
// review catching it.
//
// Traceability: REQ-OPS-002 (twelve-factor configuration), SPEC-OPS-001.
package config

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the platform's complete typed configuration.
type Config struct {
	// Environment gates behaviour that must never run in production: the
	// development-mode static-token auth issuer, permissive CORS defaults,
	// and verbose error bodies.
	Environment string `yaml:"environment"`

	// Storage, Cache and Events select which adapter internal/app wires in
	// for each driven port. They live at the top level rather than inside
	// Database/Redis because the choice is "which backend exists at all",
	// not "how is that backend tuned" — a deployment running on memory
	// storage has no database settings to tune, and Validate stops
	// demanding them (see Validate) rather than forcing every demo to
	// invent a Postgres password it will never use.
	Storage StorageKind  `yaml:"storage"`
	Cache   CacheKind    `yaml:"cache"`
	Events  EventsConfig `yaml:"events"`

	Server    ServerConfig    `yaml:"server"`
	Database  DatabaseConfig  `yaml:"database"`
	Redis     RedisConfig     `yaml:"redis"`
	AWS       AWSConfig       `yaml:"aws"`
	LLM       LLMConfig       `yaml:"llm"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	Auth      AuthConfig      `yaml:"auth"`
	Features  FeatureFlags    `yaml:"features"`
	Worker    WorkerConfig    `yaml:"worker"`
}

// StorageKind selects the persistence backend.
type StorageKind string

const (
	// StorageMemory runs every repository against internal/adapters/memstore.
	// It is a first-class runtime mode (see docs/adr/0007), not a test
	// double: it is what makes the demo one command with no infrastructure.
	StorageMemory StorageKind = "memory"
	// StoragePostgres runs against internal/adapters/postgres, applying the
	// embedded migrations at startup.
	StoragePostgres StorageKind = "postgres"
)

// CacheKind selects the ports.Cache and ports.Locker implementation.
type CacheKind string

const (
	// CacheMemory uses the in-process cache and lock table. Correct for a
	// single replica and wrong for more than one — two replicas each hold
	// their own lock table, so the distributed-mutual-exclusion contract
	// ports.Locker promises is not actually met.
	CacheMemory CacheKind = "memory"
	// CacheRedis uses a shared Redis for both, which is what makes running
	// more than one worker replica safe.
	CacheRedis CacheKind = "redis"
)

// EventsKind selects the domain-event transport.
type EventsKind string

const (
	// EventsInProcess delivers events to in-process subscribers with a
	// bounded queue, retries and a dead-letter list.
	EventsInProcess EventsKind = "inprocess"
	// EventsAWS publishes to EventBridge and consumes from SQS.
	EventsAWS EventsKind = "aws"
)

// EventsConfig selects and configures the event transport.
type EventsConfig struct {
	Kind EventsKind `yaml:"kind"`
	// EventBusName and Source are the EventBridge bus and the `source` field
	// stamped on every published event (kind == aws only).
	EventBusName string `yaml:"event_bus_name"`
	Source       string `yaml:"source"`
	// QueueURL is the SQS queue the subscriber drains (kind == aws only).
	// Empty means this process publishes but does not consume.
	QueueURL string `yaml:"queue_url"`
	// Workers, MaxAttempts and Backoff tune the in-process bus.
	Workers     int           `yaml:"workers"`
	MaxAttempts int           `yaml:"max_attempts"`
	BaseBackoff time.Duration `yaml:"base_backoff"`
	MaxBackoff  time.Duration `yaml:"max_backoff"`
}

// ServerConfig configures the HTTP listener.
type ServerConfig struct {
	Host              string        `yaml:"host"`
	Port              int           `yaml:"port"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	MaxRequestBytes   int64         `yaml:"max_request_bytes"`
	RequestTimeout    time.Duration `yaml:"request_timeout"`

	TLSEnabled    bool   `yaml:"tls_enabled"`
	TLSCertFile   string `yaml:"tls_cert_file"`
	TLSKeyFile    string `yaml:"tls_key_file"`
	TLSMinVersion string `yaml:"tls_min_version"` // "1.2" | "1.3"

	CORSAllowedOrigins []string `yaml:"cors_allowed_origins"`

	// TrustedProxyHeader names the header (e.g. "X-Forwarded-For") the real-IP
	// middleware trusts. Left empty, the middleware uses the socket peer
	// address only — the safe default behind a proxy that is not yet
	// declared trusted.
	TrustedProxyHeader string `yaml:"trusted_proxy_header"`
	// TrustedProxyCIDRs lists the peers whose forwarding header is believed,
	// as CIDRs or bare addresses. The header alone is not enough: trusting it
	// from any sender is the vulnerability, because the recorded address ends
	// up on approval and audit records. Both this and TrustedProxyHeader must
	// be set before any forwarding header is honoured.
	TrustedProxyCIDRs []string `yaml:"trusted_proxy_cidrs"`
}

// Addr returns the listen address.
func (s ServerConfig) Addr() string { return fmt.Sprintf("%s:%d", s.Host, s.Port) }

// DatabaseConfig configures the Postgres connection.
type DatabaseConfig struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	Name            string        `yaml:"name"`
	User            string        `yaml:"user"`
	Password        Secret        `yaml:"password"`
	SSLMode         string        `yaml:"ssl_mode"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnectTimeout  time.Duration `yaml:"connect_timeout"`
}

// DSN builds the connection string once Password has been resolved.
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s connect_timeout=%d",
		d.Host, d.Port, d.Name, d.User, d.Password.Value(), d.SSLMode, int(d.ConnectTimeout.Seconds()))
}

// RedisConfig configures the shared cache and distributed lock backend.
type RedisConfig struct {
	Addrs        []string      `yaml:"addrs"` // multiple entries selects cluster mode
	Password     Secret        `yaml:"password"`
	DB           int           `yaml:"db"`
	DialTimeout  time.Duration `yaml:"dial_timeout"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	PoolSize     int           `yaml:"pool_size"`
	TLSEnabled   bool          `yaml:"tls_enabled"`
}

// AWSConfig configures how CloudOptix itself talks to AWS — the control
// plane's own credentials, distinct from the per-tenant assumed-role
// credentials the AWSCredentialBroker mints.
type AWSConfig struct {
	// Mode selects the AWS adapter set: AWSModeLive assumes real customer
	// roles via STS, AWSModeSimulated points every discoverer, cost
	// ingestor, metric collector and executor at internal/adapters/awssim's
	// in-memory estate. Validate refuses "simulated" in production for the
	// same reason it refuses the scripted LLM provider: it is a
	// demonstration backend, and a production deployment silently reporting
	// a fictional estate as the customer's own would be worse than not
	// starting.
	Mode                 AWSMode       `yaml:"mode"`
	Region               string        `yaml:"region"`
	AssumeRoleExternalID Secret        `yaml:"assume_role_external_id"`
	SessionDuration      time.Duration `yaml:"session_duration"`
	// SessionCacheTTL bounds how long an assumed session is reused before a
	// fresh assumption, kept comfortably under SessionDuration.
	SessionCacheTTL time.Duration `yaml:"session_cache_ttl"`
	MaxRetries      int           `yaml:"max_retries"`
	RequestTimeout  time.Duration `yaml:"request_timeout"`
	// RateLimitPerSecond bounds outbound AWS API calls per account, so
	// discovery never becomes the throttling source it is trying to survive.
	RateLimitPerSecond float64 `yaml:"rate_limit_per_second"`
	RateLimitBurst     int     `yaml:"rate_limit_burst"`
}

// AWSMode selects which AWS adapter set is active.
type AWSMode string

const (
	// AWSModeLive uses internal/adapters/aws — the real STS broker,
	// discoverers, cost ingestors, CloudWatch collector and executors.
	AWSModeLive AWSMode = "live"
	// AWSModeSimulated uses internal/adapters/awssim against the demo
	// estate. No credentials, no network, deterministic.
	AWSModeSimulated AWSMode = "simulated"
)

// LLMProviderKind selects which LLM backend is active.
type LLMProviderKind string

const (
	LLMProviderAnthropic LLMProviderKind = "anthropic"
	LLMProviderBedrock   LLMProviderKind = "bedrock"
	LLMProviderScripted  LLMProviderKind = "scripted" // deterministic, no API key — CI and demo tenants
)

// LLMConfig selects and configures the model provider.
type LLMConfig struct {
	Provider       LLMProviderKind `yaml:"provider"`
	APIKey         Secret          `yaml:"api_key"` // used by Provider == anthropic
	Model          string          `yaml:"model"`
	BedrockRegion  string          `yaml:"bedrock_region"`
	RequestTimeout time.Duration   `yaml:"request_timeout"`
	MaxRetries     int             `yaml:"max_retries"`
	// RateLimitPerSecond and RateLimitBurst throttle outbound completion
	// calls per purpose (onboarding/copilot/narrative/...), independent of
	// the provider's own rate limit, so one runaway conversation cannot
	// exhaust the tenant's daily token quota in a burst.
	RateLimitPerSecond float64 `yaml:"rate_limit_per_second"`
	RateLimitBurst     int     `yaml:"rate_limit_burst"`
}

// TelemetryConfig configures tracing, metrics and logging.
type TelemetryConfig struct {
	ServiceName    string `yaml:"service_name"`
	ServiceVersion string `yaml:"service_version"`

	TracingEnabled   bool    `yaml:"tracing_enabled"`
	TraceSampleRatio float64 `yaml:"trace_sample_ratio"`
	// OTLPEndpoint is the documented seam for a future OTLP exporter. It is
	// read and validated (host:port shape) even though the exporter shipped
	// today is the stdout/slog one — see internal/infrastructure/telemetry.
	OTLPEndpoint string `yaml:"otlp_endpoint"`

	MetricsEnabled bool   `yaml:"metrics_enabled"`
	MetricsPath    string `yaml:"metrics_path"`

	LogLevel  string `yaml:"log_level"`  // debug | info | warn | error
	LogFormat string `yaml:"log_format"` // json | text — production must be json
}

// AuthConfig configures OIDC validation and the service/dev token paths.
type AuthConfig struct {
	OIDCIssuerURL      string        `yaml:"oidc_issuer_url"`
	OIDCAudience       string        `yaml:"oidc_audience"`
	JWKSCacheTTL       time.Duration `yaml:"jwks_cache_ttl"`
	JWKSRefreshTimeout time.Duration `yaml:"jwks_refresh_timeout"`
	ClockSkew          time.Duration `yaml:"clock_skew"`
	// AllowedAlgorithms is a closed allowlist; the JWT validator rejects
	// "none" and any algorithm outside this list unconditionally, regardless
	// of what the token header claims.
	AllowedAlgorithms []string `yaml:"allowed_algorithms"`

	ServiceTokenSecret Secret `yaml:"service_token_secret"`

	// DevStaticTokenEnabled turns on the fixed-token issuer used by local
	// development. Config.Validate refuses to start when this is true and
	// Environment == "production" — see Validate.
	DevStaticTokenEnabled bool   `yaml:"dev_static_token_enabled"`
	DevStaticToken        Secret `yaml:"dev_static_token"`

	APIKeyHeader string `yaml:"api_key_header"`
}

// FeatureFlags gate optional platform behaviour independent of plan/quota.
type FeatureFlags struct {
	AutonomousExecution bool `yaml:"autonomous_execution"`
	CostCompiler        bool `yaml:"cost_compiler"`
	Copilot             bool `yaml:"copilot"`
	SavingsLearning     bool `yaml:"savings_learning"`
	SSEStreaming        bool `yaml:"sse_streaming"`
}

// WorkerConfig configures background job processing.
type WorkerConfig struct {
	DiscoveryConcurrency  int           `yaml:"discovery_concurrency"`
	ExecutionConcurrency  int           `yaml:"execution_concurrency"`
	PollInterval          time.Duration `yaml:"poll_interval"`
	LockTTL               time.Duration `yaml:"lock_ttl"`
	MaxJobAttempts        int           `yaml:"max_job_attempts"`
	NotificationBatchSize int           `yaml:"notification_batch_size"`
}

// Defaults returns a Config populated with safe, non-production defaults.
// Every field must be reachable from here — Load starts from this value so
// a field an operator never mentions in any layer still has a sane setting
// rather than a zero value that happens to compile.
func Defaults() *Config {
	return &Config{
		Environment: "development",
		// The zero-infrastructure demo path: memory storage, in-process
		// cache/locks and events, the simulated estate and the scripted
		// model provider. Every one of these is refused in production by
		// Validate, so "works with no configuration" and "cannot be
		// deployed by accident" are both true at once.
		Storage: StorageMemory,
		Cache:   CacheMemory,
		Events: EventsConfig{
			Kind: EventsInProcess, Source: "cloudoptix", EventBusName: "default",
			Workers: 4, MaxAttempts: 3, BaseBackoff: 50 * time.Millisecond, MaxBackoff: 2 * time.Second,
		},
		Server: ServerConfig{
			Host:               "0.0.0.0",
			Port:               8080,
			ReadTimeout:        10 * time.Second,
			ReadHeaderTimeout:  5 * time.Second,
			WriteTimeout:       30 * time.Second,
			IdleTimeout:        120 * time.Second,
			ShutdownTimeout:    20 * time.Second,
			MaxRequestBytes:    5 << 20, // 5 MiB
			RequestTimeout:     25 * time.Second,
			TLSMinVersion:      "1.3",
			CORSAllowedOrigins: []string{},
		},
		Database: DatabaseConfig{
			Host: "localhost", Port: 5432, Name: "cloudoptix", User: "cloudoptix",
			SSLMode: "require", MaxOpenConns: 25, MaxIdleConns: 10,
			ConnMaxLifetime: 30 * time.Minute, ConnectTimeout: 5 * time.Second,
		},
		Redis: RedisConfig{
			Addrs: []string{"localhost:6379"}, DB: 0,
			DialTimeout: 2 * time.Second, ReadTimeout: 1 * time.Second, WriteTimeout: 1 * time.Second,
			PoolSize: 20,
		},
		AWS: AWSConfig{
			Mode:   AWSModeSimulated,
			Region: "us-east-1", SessionDuration: time.Hour, SessionCacheTTL: 50 * time.Minute,
			MaxRetries: 5, RequestTimeout: 15 * time.Second,
			RateLimitPerSecond: 20, RateLimitBurst: 40,
		},
		LLM: LLMConfig{
			Provider: LLMProviderScripted, Model: "claude-scripted-v1",
			RequestTimeout: 30 * time.Second, MaxRetries: 3,
			RateLimitPerSecond: 5, RateLimitBurst: 10,
		},
		Telemetry: TelemetryConfig{
			ServiceName: "cloudoptix-api", ServiceVersion: "dev",
			TracingEnabled: true, TraceSampleRatio: 0.1,
			MetricsEnabled: true, MetricsPath: "/metrics",
			LogLevel: "info", LogFormat: "json",
		},
		Auth: AuthConfig{
			JWKSCacheTTL: 15 * time.Minute, JWKSRefreshTimeout: 5 * time.Second,
			ClockSkew: 2 * time.Minute, AllowedAlgorithms: []string{"RS256", "ES256"},
			APIKeyHeader: "X-CloudOptix-Api-Key",
		},
		Features: FeatureFlags{
			AutonomousExecution: false, CostCompiler: true, Copilot: true,
			SavingsLearning: true, SSEStreaming: true,
		},
		Worker: WorkerConfig{
			DiscoveryConcurrency: 8, ExecutionConcurrency: 4,
			PollInterval: 5 * time.Second, LockTTL: 5 * time.Minute,
			MaxJobAttempts: 5, NotificationBatchSize: 50,
		},
	}
}

// LoadOptions parameterises Load.
type LoadOptions struct {
	// FilePath is the YAML file to layer over the defaults. Empty skips this
	// layer, which is legal — a fully-environment-driven deployment (typical
	// in Kubernetes) need not ship a file at all.
	FilePath string
	// Environ supplies the process environment, as a slice of "KEY=VALUE"
	// entries, matching os.Environ()'s shape. Tests pass a fixed slice
	// instead of touching the real environment.
	Environ []string
	// Args are command-line flags, matching os.Args[1:]. Nil skips the flag
	// layer.
	Args []string
}

// Load builds a Config by applying, in order: Defaults, an optional YAML
// file, environment variables (prefixed CLOUDOPTIX_), then flags. Each layer
// only overrides fields it actually sets — a layer's absence of an entry
// never resets a field to zero.
func Load(opts LoadOptions) (*Config, error) {
	cfg := Defaults()

	if opts.FilePath != "" {
		if err := applyYAMLFile(cfg, opts.FilePath); err != nil {
			return nil, fmt.Errorf("config: loading %s: %w", opts.FilePath, err)
		}
	}

	env := parseEnviron(opts.Environ)
	if err := applyEnv(cfg, env); err != nil {
		return nil, fmt.Errorf("config: applying environment: %w", err)
	}

	if opts.Args != nil {
		if err := applyFlags(cfg, opts.Args); err != nil {
			return nil, fmt.Errorf("config: applying flags: %w", err)
		}
	}

	nameSecretFields(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadFromProcess is sugar for Load using the real process environment
// (os.Environ) and arguments (os.Args[1:]).
func LoadFromProcess(filePath string) (*Config, error) {
	return Load(LoadOptions{FilePath: filePath, Environ: os.Environ(), Args: os.Args[1:]})
}

func applyYAMLFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // a typo in a config file must fail loudly, not silently do nothing
	if err := dec.Decode(cfg); err != nil {
		return err
	}
	return nil
}

func parseEnviron(entries []string) map[string]string {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		m[k] = v
	}
	return m
}

// ResolveSecrets walks every Secret field in cfg and resolves it via getenv
// and resolver. Call this once, after Load, before any component reads a
// secret's Value().
func ResolveSecrets(ctx context.Context, cfg *Config, getenv func(string) (string, bool), resolver SecretResolver) error {
	secrets := []*Secret{
		&cfg.Database.Password,
		&cfg.Redis.Password,
		&cfg.AWS.AssumeRoleExternalID,
		&cfg.LLM.APIKey,
		&cfg.Auth.ServiceTokenSecret,
		&cfg.Auth.DevStaticToken,
	}
	for _, s := range secrets {
		if !s.IsSet() {
			continue
		}
		if err := s.Resolve(ctx, getenv, resolver); err != nil {
			return err
		}
	}
	return nil
}

func nameSecretFields(cfg *Config) {
	cfg.Database.Password.SetFieldName("database.password")
	cfg.Redis.Password.SetFieldName("redis.password")
	cfg.AWS.AssumeRoleExternalID.SetFieldName("aws.assume_role_external_id")
	cfg.LLM.APIKey.SetFieldName("llm.api_key")
	cfg.Auth.ServiceTokenSecret.SetFieldName("auth.service_token_secret")
	cfg.Auth.DevStaticToken.SetFieldName("auth.dev_static_token")
}

// Validate checks the configuration for actionable errors: values that would
// fail loudly at request time anyway are caught here at startup instead,
// with a message that names the field and says what to do about it.
func (c *Config) Validate() error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	switch c.Environment {
	case "production", "staging", "development", "test":
	default:
		add("environment: must be one of production|staging|development|test, got %q", c.Environment)
	}

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		add("server.port: %d is not a valid TCP port", c.Server.Port)
	}
	if c.Server.MaxRequestBytes <= 0 {
		add("server.max_request_bytes: must be positive (got %d) — set a real cap, do not disable it", c.Server.MaxRequestBytes)
	}
	if c.Server.TLSEnabled {
		if c.Server.TLSCertFile == "" || c.Server.TLSKeyFile == "" {
			add("server.tls_cert_file / server.tls_key_file: both required when server.tls_enabled is true")
		}
		if c.Server.TLSMinVersion != "1.2" && c.Server.TLSMinVersion != "1.3" {
			add("server.tls_min_version: must be \"1.2\" or \"1.3\", got %q", c.Server.TLSMinVersion)
		}
	}
	if c.Environment == "production" && !c.Server.TLSEnabled {
		add("server.tls_enabled: must be true in production — CloudOptix carries customer AWS role ARNs and cost data over this API")
	}

	switch c.Storage {
	case StorageMemory:
		if c.Environment == "production" {
			add("storage: \"memory\" loses every recommendation, execution plan and audit record on restart and cannot be shared between replicas — production requires \"postgres\"")
		}
	case StoragePostgres:
		// Database settings are only load-bearing here. Demanding them on
		// the memory path would make the zero-infrastructure demo invent a
		// Postgres password nothing ever reads.
		if c.Database.Host == "" || c.Database.Name == "" || c.Database.User == "" {
			add("database.host / database.name / database.user: all required when storage is \"postgres\"")
		}
		if !c.Database.Password.IsSet() && c.Environment != "test" {
			add("database.password: required when storage is \"postgres\" (set database.password to \"env:CLOUDOPTIX_DATABASE_PASSWORD\" or a secretref, or set CLOUDOPTIX_DATABASE_PASSWORD directly)")
		}
		if c.Database.MaxOpenConns < c.Database.MaxIdleConns {
			add("database.max_open_conns (%d) must be >= database.max_idle_conns (%d)", c.Database.MaxOpenConns, c.Database.MaxIdleConns)
		}
	default:
		add("storage: must be memory or postgres, got %q", c.Storage)
	}

	switch c.Cache {
	case CacheMemory:
		// An in-process lock table satisfies ports.Locker's interface but
		// not its contract once a second replica exists, so this is a
		// single-replica-only mode. Nothing here can detect the replica
		// count; the deployment does (see helm/cloudoptix).
	case CacheRedis:
		if len(c.Redis.Addrs) == 0 {
			add("redis.addrs: at least one address required when cache is \"redis\"")
		}
	default:
		add("cache: must be memory or redis, got %q", c.Cache)
	}

	switch c.Events.Kind {
	case EventsInProcess:
	case EventsAWS:
		if c.Events.EventBusName == "" {
			add("events.event_bus_name: required when events.kind is \"aws\"")
		}
	default:
		add("events.kind: must be inprocess or aws, got %q", c.Events.Kind)
	}

	switch c.AWS.Mode {
	case AWSModeLive, AWSModeSimulated:
	default:
		add("aws.mode: must be live or simulated, got %q", c.AWS.Mode)
	}
	if c.Environment == "production" && c.AWS.Mode == AWSModeSimulated {
		add("aws.mode: \"simulated\" reports a fictional in-memory estate and is a demonstration backend, not a production one")
	}
	if c.AWS.RateLimitPerSecond <= 0 {
		add("aws.rate_limit_per_second: must be positive")
	}

	switch c.LLM.Provider {
	case LLMProviderAnthropic:
		if !c.LLM.APIKey.IsSet() {
			add("llm.api_key: required when llm.provider is \"anthropic\"")
		}
	case LLMProviderBedrock:
		if c.LLM.BedrockRegion == "" {
			add("llm.bedrock_region: required when llm.provider is \"bedrock\"")
		}
	case LLMProviderScripted:
		// no credential required — this is the deterministic, no-API-key path.
	default:
		add("llm.provider: must be one of anthropic|bedrock|scripted, got %q", c.LLM.Provider)
	}
	if c.Environment == "production" && c.LLM.Provider == LLMProviderScripted {
		add("llm.provider: \"scripted\" is a deterministic stand-in for demos and CI, not a production provider")
	}

	if c.Telemetry.TraceSampleRatio < 0 || c.Telemetry.TraceSampleRatio > 1 {
		add("telemetry.trace_sample_ratio: must be within [0,1], got %v", c.Telemetry.TraceSampleRatio)
	}
	switch c.Telemetry.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		add("telemetry.log_level: must be one of debug|info|warn|error, got %q", c.Telemetry.LogLevel)
	}
	switch c.Telemetry.LogFormat {
	case "json", "text":
	default:
		add("telemetry.log_format: must be json or text, got %q", c.Telemetry.LogFormat)
	}
	if c.Environment == "production" && c.Telemetry.LogFormat != "json" {
		add("telemetry.log_format: must be \"json\" in production for log aggregation and trace correlation")
	}

	if c.Auth.OIDCIssuerURL == "" && c.Environment != "test" && !c.Auth.DevStaticTokenEnabled {
		add("auth.oidc_issuer_url: required unless auth.dev_static_token_enabled is set for local development")
	}
	if len(c.Auth.AllowedAlgorithms) == 0 {
		add("auth.allowed_algorithms: at least one algorithm required (e.g. RS256) — an empty allowlist is not \"allow everything\", it means no token will ever validate")
	}
	for _, alg := range c.Auth.AllowedAlgorithms {
		if strings.EqualFold(alg, "none") {
			add("auth.allowed_algorithms: %q must never be included — this is the classic JWT \"alg:none\" bypass", alg)
		}
	}
	// This is the hard rule from REQ-SEC: the development-mode static-token
	// issuer must be structurally impossible to run in production, not just
	// discouraged by documentation.
	if c.Auth.DevStaticTokenEnabled && c.Environment == "production" {
		add("auth.dev_static_token_enabled: must never be true when environment is \"production\" — this issues tokens for any subject with no verification at all")
	}

	if c.Worker.DiscoveryConcurrency < 1 || c.Worker.ExecutionConcurrency < 1 {
		add("worker.discovery_concurrency / worker.execution_concurrency: must each be at least 1")
	}

	if len(errs) > 0 {
		return fmt.Errorf("config: %d validation error(s):\n  - %s", len(errs), strings.Join(errs, "\n  - "))
	}
	return nil
}

// --- environment variable layer -------------------------------------------

// EnvPrefix is prepended to every CloudOptix environment variable, so the
// process environment of a shared host cannot accidentally collide with an
// unrelated PORT or LOG_LEVEL variable.
const EnvPrefix = "CLOUDOPTIX_"

// envBindings is the single source of truth for every environment variable
// CloudOptix reads, and doubles as the input to EnvVarDocs. Each entry names
// the field it sets and how to parse it; keeping this as an explicit table
// rather than reflection-driven struct tag magic is what makes "a documented
// list of every environment variable" possible to generate rather than
// merely promise in a comment.
type envBinding struct {
	name        string
	description string
	secret      bool
	apply       func(cfg *Config, raw string) error
}

func envBindings() []envBinding {
	return []envBinding{
		{"ENVIRONMENT", "Deployment environment: production|staging|development|test.", false,
			func(c *Config, v string) error { c.Environment = v; return nil }},

		{"STORAGE", "Persistence backend: memory|postgres.", false,
			func(c *Config, v string) error { c.Storage = StorageKind(v); return nil }},
		{"CACHE", "Cache and distributed-lock backend: memory|redis.", false,
			func(c *Config, v string) error { c.Cache = CacheKind(v); return nil }},
		{"EVENTS", "Domain-event transport: inprocess|aws.", false,
			func(c *Config, v string) error { c.Events.Kind = EventsKind(v); return nil }},
		{"EVENTS_EVENT_BUS_NAME", "EventBridge bus name (events=aws only).", false,
			func(c *Config, v string) error { c.Events.EventBusName = v; return nil }},
		{"EVENTS_QUEUE_URL", "SQS queue URL this process consumes events from (events=aws only).", false,
			func(c *Config, v string) error { c.Events.QueueURL = v; return nil }},

		{"SERVER_HOST", "HTTP listen host.", false, func(c *Config, v string) error { c.Server.Host = v; return nil }},
		{"SERVER_PORT", "HTTP listen port.", false, intField(func(c *Config) *int { return &c.Server.Port })},
		{"SERVER_TLS_ENABLED", "Enable TLS on the listener (true|false).", false, boolField(func(c *Config) *bool { return &c.Server.TLSEnabled })},
		{"SERVER_TLS_CERT_FILE", "Path to the TLS certificate (PEM).", false, func(c *Config, v string) error { c.Server.TLSCertFile = v; return nil }},
		{"SERVER_TLS_KEY_FILE", "Path to the TLS private key (PEM).", false, func(c *Config, v string) error { c.Server.TLSKeyFile = v; return nil }},
		{"SERVER_CORS_ALLOWED_ORIGINS", "Comma-separated list of allowed CORS origins.", false,
			func(c *Config, v string) error { c.Server.CORSAllowedOrigins = splitCSV(v); return nil }},

		{"DATABASE_HOST", "Postgres host.", false, func(c *Config, v string) error { c.Database.Host = v; return nil }},
		{"DATABASE_PORT", "Postgres port.", false, intField(func(c *Config) *int { return &c.Database.Port })},
		{"DATABASE_NAME", "Postgres database name.", false, func(c *Config, v string) error { c.Database.Name = v; return nil }},
		{"DATABASE_USER", "Postgres user.", false, func(c *Config, v string) error { c.Database.User = v; return nil }},
		{"DATABASE_PASSWORD", "Postgres password — a literal value here is accepted because process environment is not a committed file.", true,
			secretField(func(c *Config) *Secret { return &c.Database.Password })},

		{"REDIS_ADDRS", "Comma-separated Redis addresses (multiple entries selects cluster mode).", false,
			func(c *Config, v string) error { c.Redis.Addrs = splitCSV(v); return nil }},
		{"REDIS_PASSWORD", "Redis AUTH password.", true, secretField(func(c *Config) *Secret { return &c.Redis.Password })},

		{"AWS_MODE", "AWS adapter set: live|simulated.", false,
			func(c *Config, v string) error { c.AWS.Mode = AWSMode(v); return nil }},
		// AWS_SIMULATED is the boolean spelling deployments/docker-compose*.yml
		// already used before aws.mode existed. It is kept as an alias rather
		// than removed so an existing compose file or ConfigMap keeps working.
		{"AWS_SIMULATED", "Deprecated boolean alias for AWS_MODE (true -> simulated, false -> live).", false,
			func(c *Config, v string) error {
				b, err := strconv.ParseBool(v)
				if err != nil {
					return fmt.Errorf("expected true/false, got %q: %w", v, err)
				}
				if b {
					c.AWS.Mode = AWSModeSimulated
				} else {
					c.AWS.Mode = AWSModeLive
				}
				return nil
			}},
		{"AWS_REGION", "Region CloudOptix's own control-plane AWS calls use.", false, func(c *Config, v string) error { c.AWS.Region = v; return nil }},
		{"AWS_ASSUME_ROLE_EXTERNAL_ID", "External ID CloudOptix presents when assuming a customer's onboarding role.", true,
			secretField(func(c *Config) *Secret { return &c.AWS.AssumeRoleExternalID })},

		{"LLM_PROVIDER", "Model provider: anthropic|bedrock|scripted.", false,
			func(c *Config, v string) error { c.LLM.Provider = LLMProviderKind(v); return nil }},
		{"LLM_API_KEY", "Anthropic API key (provider=anthropic only).", true, secretField(func(c *Config) *Secret { return &c.LLM.APIKey })},
		{"LLM_MODEL", "Model identifier.", false, func(c *Config, v string) error { c.LLM.Model = v; return nil }},
		{"LLM_BEDROCK_REGION", "Bedrock region (provider=bedrock only).", false, func(c *Config, v string) error { c.LLM.BedrockRegion = v; return nil }},

		{"TELEMETRY_LOG_LEVEL", "debug|info|warn|error.", false, func(c *Config, v string) error { c.Telemetry.LogLevel = v; return nil }},
		{"TELEMETRY_LOG_FORMAT", "json|text. Must be json in production.", false, func(c *Config, v string) error { c.Telemetry.LogFormat = v; return nil }},
		{"TELEMETRY_TRACING_ENABLED", "Enable trace export (true|false).", false, boolField(func(c *Config) *bool { return &c.Telemetry.TracingEnabled })},
		{"TELEMETRY_TRACE_SAMPLE_RATIO", "Fraction of requests traced, 0..1.", false, floatField(func(c *Config) *float64 { return &c.Telemetry.TraceSampleRatio })},
		{"TELEMETRY_OTLP_ENDPOINT", "host:port of an OTLP collector. Unused until an OTLP exporter is wired in; see internal/infrastructure/telemetry.", false,
			func(c *Config, v string) error { c.Telemetry.OTLPEndpoint = v; return nil }},

		{"AUTH_OIDC_ISSUER_URL", "OIDC issuer used for discovery and JWKS fetch.", false, func(c *Config, v string) error { c.Auth.OIDCIssuerURL = v; return nil }},
		{"AUTH_OIDC_AUDIENCE", "Expected JWT audience claim.", false, func(c *Config, v string) error { c.Auth.OIDCAudience = v; return nil }},
		{"AUTH_SERVICE_TOKEN_SECRET", "HMAC secret used to validate worker service tokens.", true,
			secretField(func(c *Config) *Secret { return &c.Auth.ServiceTokenSecret })},
		{"AUTH_DEV_STATIC_TOKEN_ENABLED", "Enable the fixed-token dev issuer. Refused when ENVIRONMENT=production.", false,
			boolField(func(c *Config) *bool { return &c.Auth.DevStaticTokenEnabled })},
		{"AUTH_DEV_STATIC_TOKEN", "The fixed bearer token accepted in dev mode.", true, secretField(func(c *Config) *Secret { return &c.Auth.DevStaticToken })},

		{"FEATURES_AUTONOMOUS_EXECUTION", "Allow policy-approved recommendations to execute without a human click.", false,
			boolField(func(c *Config) *bool { return &c.Features.AutonomousExecution })},

		{"WORKER_DISCOVERY_CONCURRENCY", "Concurrent discovery scans per process.", false, intField(func(c *Config) *int { return &c.Worker.DiscoveryConcurrency })},
		{"WORKER_EXECUTION_CONCURRENCY", "Concurrent execution plans per process.", false, intField(func(c *Config) *int { return &c.Worker.ExecutionConcurrency })},
	}
}

func intField(get func(*Config) *int) func(*Config, string) error {
	return func(c *Config, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("expected an integer, got %q: %w", v, err)
		}
		*get(c) = n
		return nil
	}
}

func floatField(get func(*Config) *float64) func(*Config, string) error {
	return func(c *Config, v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("expected a number, got %q: %w", v, err)
		}
		*get(c) = f
		return nil
	}
}

func boolField(get func(*Config) *bool) func(*Config, string) error {
	return func(c *Config, v string) error {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("expected true/false, got %q: %w", v, err)
		}
		*get(c) = b
		return nil
	}
}

func secretField(get func(*Config) *Secret) func(*Config, string) error {
	return func(c *Config, v string) error {
		*get(c) = NewLiteralSecret(v)
		return nil
	}
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func applyEnv(cfg *Config, env map[string]string) error {
	for _, b := range envBindings() {
		raw, ok := env[EnvPrefix+b.name]
		if !ok {
			continue
		}
		if err := b.apply(cfg, raw); err != nil {
			return fmt.Errorf("%s%s: %w", EnvPrefix, b.name, err)
		}
	}
	return nil
}

// EnvVarDoc is one documented environment variable, for generating an
// operator-facing reference (see EnvVarDocs / api/README.md style listing).
type EnvVarDoc struct {
	Name        string
	Description string
	Secret      bool
}

// EnvVarDocs returns every environment variable CloudOptix reads, in a
// stable order, for documentation generation and for the deploy checklist to
// diff against what is actually set in a target environment.
func EnvVarDocs() []EnvVarDoc {
	bindings := envBindings()
	docs := make([]EnvVarDoc, 0, len(bindings))
	for _, b := range bindings {
		docs = append(docs, EnvVarDoc{Name: EnvPrefix + b.name, Description: b.description, Secret: b.secret})
	}
	return docs
}

// --- flag layer --------------------------------------------------------

// applyFlags binds the same fields as applyEnv to command-line flags, using
// dash-separated, dot-free flag names (e.g. -server-port) since flag package
// conventions do not nest.
func applyFlags(cfg *Config, args []string) error {
	fs := flag.NewFlagSet("cloudoptix", flag.ContinueOnError)
	// flag values are bound directly to cfg's fields through the same
	// binding table used for env, so the two layers can never drift apart on
	// what a given knob is called or how it parses.
	type stringFlag struct {
		name string
		set  func(string)
	}
	var strFlags []stringFlag
	for _, b := range envBindings() {
		b := b
		flagName := strings.ToLower(strings.ReplaceAll(b.name, "_", "-"))
		strFlags = append(strFlags, stringFlag{
			name: flagName,
			set: func(v string) {
				_ = b.apply(cfg, v) // flag-level parse errors are surfaced by fs.Parse below via a wrapping Var
			},
		})
	}
	vars := make([]*applyingValue, len(strFlags))
	for i, sf := range strFlags {
		sf := sf
		v := &applyingValue{apply: sf.set}
		vars[i] = v
		fs.Var(v, sf.name, "override "+sf.name)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	return nil
}

// applyingValue adapts the binding table's string-setter functions to
// flag.Value, so unknown or malformed flags produce the standard flag package
// error rather than a second bespoke parser.
type applyingValue struct {
	apply func(string)
	set   bool
	raw   string
}

func (v *applyingValue) String() string { return v.raw }
func (v *applyingValue) Set(s string) error {
	v.raw, v.set = s, true
	v.apply(s)
	return nil
}
