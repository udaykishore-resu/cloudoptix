package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/events"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/auth"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/resilience"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// eventSet is the resolved event transport. subscriber is nil when this
// deployment publishes but does not consume — the API process on the AWS
// transport, for instance, where the workers own consumption.
type eventSet struct {
	publisher  ports.EventPublisher
	subscriber ports.EventSubscriber
	close      func() error
}

// buildEvents selects the domain-event transport.
func buildEvents(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*eventSet, error) {
	switch cfg.Events.Kind {
	case config.EventsInProcess:
		opts := []events.Option{events.WithLogger(logger)}
		if cfg.Events.Workers > 0 {
			opts = append(opts, events.WithWorkers(cfg.Events.Workers))
		}
		if cfg.Events.MaxAttempts > 0 {
			base, max := cfg.Events.BaseBackoff, cfg.Events.MaxBackoff
			if base <= 0 {
				base = 50 * time.Millisecond
			}
			if max <= 0 {
				max = 2 * time.Second
			}
			opts = append(opts, events.WithRetry(cfg.Events.MaxAttempts, base, max))
		}
		bus := events.New(opts...)
		return &eventSet{
			publisher: bus, subscriber: bus,
			close: func() error { bus.Close(); return nil },
		}, nil

	case config.EventsAWS:
		base, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("app: resolving AWS credentials for the event bus: %w", err)
		}
		if cfg.AWS.Region != "" {
			base.Region = cfg.AWS.Region
		}
		source := cfg.Events.Source
		if source == "" {
			source = "cloudoptix"
		}
		pub := events.NewEventBridgePublisher(base, cfg.Events.EventBusName, source, logger)
		set := &eventSet{publisher: pub, close: func() error { return nil }}
		if cfg.Events.QueueURL != "" {
			set.subscriber = events.NewSQSSubscriber(base, cfg.Events.QueueURL, logger)
		}
		return set, nil

	default:
		return nil, fmt.Errorf("app: unknown events.kind %q (want %q or %q)",
			cfg.Events.Kind, config.EventsInProcess, config.EventsAWS)
	}
}

// buildAuthenticator assembles every credential path the deployment has
// configured. Each is optional and independently nil-able; the
// authenticator's own Authenticate handles a nil path by simply not
// offering it, so a deployment with only OIDC and a deployment with only a
// dev token both work without this function branching on the combination.
func buildAuthenticator(ctx context.Context, cfg *config.Config, repos ports.Repositories) (*auth.Authenticator, error) {
	a := &auth.Authenticator{
		Users:        userLookup{users: repos.Users},
		Clock:        systemClock{},
		APIKeyHeader: cfg.Auth.APIKeyHeader,
		APIKeyTTL:    time.Hour,
	}

	if cfg.Auth.OIDCIssuerURL != "" {
		// The JWKS URL is derived by the standard OIDC discovery convention
		// rather than fetched at startup: a network call here would make an
		// identity provider's momentary unavailability into a failure to
		// start, and the cache refreshes lazily on first use anyway.
		jwksURL := strings.TrimSuffix(cfg.Auth.OIDCIssuerURL, "/") + "/.well-known/jwks.json"
		if discovered, err := auth.DiscoverJWKSURL(ctx, cfg.Auth.OIDCIssuerURL,
			&http.Client{Timeout: cfg.Auth.JWKSRefreshTimeout}); err == nil && discovered != "" {
			jwksURL = discovered
		}
		v, err := auth.NewValidator(auth.ValidatorConfig{
			Issuer:            cfg.Auth.OIDCIssuerURL,
			Audience:          cfg.Auth.OIDCAudience,
			AllowedAlgorithms: cfg.Auth.AllowedAlgorithms,
			ClockSkew:         cfg.Auth.ClockSkew,
			JWKS: auth.NewJWKSCache(jwksURL, cfg.Auth.JWKSCacheTTL,
				&http.Client{Timeout: cfg.Auth.JWKSRefreshTimeout}),
		})
		if err != nil {
			return nil, fmt.Errorf("app: configuring OIDC validation for issuer %s: %w", cfg.Auth.OIDCIssuerURL, err)
		}
		a.OIDC = v
	}

	if cfg.Auth.ServiceTokenSecret.IsSet() {
		issuer, err := auth.NewServiceTokenIssuer(cfg.Auth.ServiceTokenSecret.Value(), time.Hour)
		if err != nil {
			return nil, fmt.Errorf("app: configuring the service-token issuer: %w", err)
		}
		a.Service = issuer
	}

	if cfg.Auth.DevStaticTokenEnabled {
		// NewDevIssuer refuses to build in production; Config.Validate has
		// already refused to load such a configuration at all, so reaching
		// here in production is impossible. Both checks stay: one is a
		// configuration error, the other is a construction invariant, and
		// removing either would leave the guarantee resting on a single
		// point.
		dev, err := auth.NewDevIssuer(cfg.Environment, cfg.Auth.DevStaticToken.Value(),
			DemoTenantID, devTokenRoles())
		if err != nil {
			return nil, fmt.Errorf("app: configuring the development static-token issuer: %w", err)
		}
		a.Dev = dev
	}

	return a, nil
}

// resilienceLimiter builds the per-tenant HTTP rate limiter, or nil when the
// deployment has not configured one. It is derived from the AWS rate limit
// only in the sense of sharing its shape; the API's own ceiling is
// deliberately generous, because its job is containing a runaway client, not
// shaping normal traffic.
func resilienceLimiter(cfg *config.Config) *resilience.KeyedLimiter {
	const requestsPerSecond, burst = 50, 200
	if cfg.Environment == "test" {
		return nil
	}
	return resilience.NewKeyedLimiter(requestsPerSecond, burst)
}
