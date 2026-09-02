package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/awssim"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/server"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/telemetry"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
	transporthttp "github.com/udaykishore-resu/cloudoptix/internal/transport/http"
)

// App is a fully wired CloudOptix process: every adapter constructed, every
// application service assembled, the HTTP router built and the background
// workers registered but not started.
//
// Workers are registered rather than started because the same App serves
// three binaries with different jobs: cloudoptix-api runs the router and no
// workers, cloudoptix-worker runs a selected subset and no listener, and the
// end-to-end tests drive the services directly and run neither. Deciding
// *what exists* at Build time and *what runs* at the call site keeps those
// three from needing three different wirings.
type App struct {
	Config *config.Config
	Logger *slog.Logger

	Services     ports.Services
	Repositories ports.Repositories
	UnitOfWork   ports.UnitOfWork

	Router  http.Handler
	Health  *server.Health
	Metrics *telemetry.Metrics

	// Workers is the full catalog this process could run, keyed by name. See
	// workers.go; cmd/cloudoptix-worker selects from it.
	Workers map[string]*Worker

	// Estate is the simulated AWS estate, non-nil only in
	// config.AWSModeSimulated. Tests assert against it directly — that the
	// executed plan actually mutated the estate, that the estate's cost fell
	// — which is the difference between proving an optimization ran and
	// proving it worked.
	Estate *awssim.Estate
	// Pricing is the catalog every counterfactual, compiler run and
	// rightsizing candidate is priced against.
	Pricing ports.PricingCatalog

	// Events is the publisher every service writes to; Subscriber is the
	// consuming side, nil when this transport has no consumer configured.
	Events     ports.EventPublisher
	Subscriber ports.EventSubscriber

	Cache  ports.Cache
	Locker ports.Locker

	// LLM is the fully-decorated provider (middleware chain plus the
	// fallback-to-deterministic wrapper), exposed so a health check and the
	// copilot report the same provider.
	LLM ports.LLMProvider

	// Knowledge is the RAG index, seeded with the platform corpus at Build.
	Knowledge ports.KnowledgeStore

	// services holds the concrete service values for the few worker entry
	// points ports.Services deliberately does not expose. See services.go.
	services *serviceSet

	closers []func() error
}

// Build constructs everything from cfg. It returns an error rather than
// panicking on any misconfiguration a human could plausibly produce, so the
// binaries can exit non-zero with a message instead of starting
// half-configured and failing on the first request.
//
// The construction order is forced by the dependency graph and is worth
// reading as documentation of it: telemetry, then storage, then the AWS
// adapter set, then the LLM chain, then events and cache, then the
// application services, then the transport. Nothing later is needed by
// anything earlier.
func Build(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*App, error) {
	if cfg == nil {
		return nil, errors.New("app: a *config.Config is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	app := &App{Config: cfg, Logger: logger, Pricing: pricing.New()}

	if cfg.Telemetry.MetricsEnabled {
		app.Metrics = telemetry.NewMetrics()
	}

	storage, err := buildStorage(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}
	app.Repositories = storage.repos
	app.UnitOfWork = storage.uow
	app.closers = append(app.closers, storage.close)

	cacheSet, err := buildCache(ctx, cfg)
	if err != nil {
		app.closeAll()
		return nil, err
	}
	app.Cache, app.Locker = cacheSet.cache, cacheSet.locker
	app.closers = append(app.closers, cacheSet.close)

	events, err := buildEvents(ctx, cfg, logger)
	if err != nil {
		app.closeAll()
		return nil, err
	}
	app.Events, app.Subscriber = events.publisher, events.subscriber
	app.closers = append(app.closers, events.close)

	awsSet, err := buildAWS(ctx, cfg, app.Pricing, logger)
	if err != nil {
		app.closeAll()
		return nil, err
	}
	app.Estate = awsSet.estate

	llm, err := buildLLM(cfg, app.Metrics, app.Cache, logger)
	if err != nil {
		app.closeAll()
		return nil, err
	}
	app.LLM = llm

	knowledge, err := buildKnowledge(ctx, llm, logger)
	if err != nil {
		app.closeAll()
		return nil, err
	}
	app.Knowledge = knowledge

	svcs, err := buildServices(cfg, app, awsSet, logger)
	if err != nil {
		app.closeAll()
		return nil, err
	}
	app.Services = svcs

	app.Health = buildHealth(cfg, app, storage, cacheSet)

	router, err := buildRouter(ctx, cfg, app, logger)
	if err != nil {
		app.closeAll()
		return nil, err
	}
	app.Router = router

	app.Workers = buildWorkers(cfg, app, logger)

	return app, nil
}

// Close releases every resource Build acquired, in reverse construction
// order. It is safe to call more than once.
func (a *App) Close() error { return a.closeAll() }

func (a *App) closeAll() error {
	var errs []error
	for i := len(a.closers) - 1; i >= 0; i-- {
		if a.closers[i] == nil {
			continue
		}
		if err := a.closers[i](); err != nil {
			errs = append(errs, err)
		}
	}
	a.closers = nil
	return errors.Join(errs...)
}

// buildRouter assembles the HTTP surface. The OpenAPI document is read once
// here rather than per request (see transport/http.serveOpenAPISpec); a
// missing file is logged and left empty, which renders 404 on
// /openapi.yaml, rather than failing startup — an API that serves traffic
// without its own description is degraded, not broken.
func buildRouter(ctx context.Context, cfg *config.Config, app *App, logger *slog.Logger) (http.Handler, error) {
	authenticator, err := buildAuthenticator(ctx, cfg, app.Repositories)
	if err != nil {
		return nil, err
	}

	var limiter = resilienceLimiter(cfg)

	// A malformed trusted-proxy entry must stop startup rather than silently
	// shrink the trusted set: the failure mode is that a real proxy stops
	// being trusted, forwarding headers get ignored, and every audit record
	// quietly starts naming the load balancer instead of the caller.
	trustedProxies, err := transporthttp.ParseTrustedProxies(cfg.Server.TrustedProxyCIDRs)
	if err != nil {
		return nil, fmt.Errorf("server.trusted_proxy_cidrs: %w", err)
	}

	deps := transporthttp.Deps{
		Services:       app.Services,
		Auth:           authenticator,
		Metrics:        app.Metrics,
		Logger:         logger,
		Idempotency:    transporthttp.NewMemoryIdempotencyStore(24 * time.Hour),
		RateLimiter:    limiter,
		MaxBodyBytes:   cfg.Server.MaxRequestBytes,
		RequestTimeout: cfg.Server.RequestTimeout,
		CORSOrigins:    cfg.Server.CORSAllowedOrigins,
		TrustedProxyHeader: cfg.Server.TrustedProxyHeader,
		TrustedProxies:     trustedProxies,
		AuditEnabled:   true,
		OpenAPISpec:    loadOpenAPISpec(logger),
	}
	return transporthttp.NewRouter(deps, app.Health), nil
}

// loadOpenAPISpec reads api/openapi.yaml relative to the working directory,
// then relative to the executable — a container image built from
// deployments/docker/Dockerfile has neither, so an empty result is normal
// there and must not be fatal.
func loadOpenAPISpec(logger *slog.Logger) []byte {
	candidates := []string{"api/openapi.yaml", "openapi.yaml"}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "openapi.yaml"),
			filepath.Join(dir, "api", "openapi.yaml"))
	}
	for _, c := range candidates {
		if data, err := os.ReadFile(c); err == nil && len(data) > 0 {
			return data
		}
	}
	logger.Debug("openapi document not found; GET /openapi.yaml will return 404",
		slog.Any("searched", candidates))
	return nil
}

// Describe renders the resolved adapter selection as log attributes. Every
// binary logs this at startup so "which backend is this pod actually using"
// is answerable from the logs rather than by inspecting the environment of a
// running container.
func (a *App) Describe() []slog.Attr {
	awsMode := string(a.Config.AWS.Mode)
	if a.Estate != nil {
		awsMode = fmt.Sprintf("%s(%s, %d regions)", awsMode, a.Estate.AccountID, len(a.Estate.Regions))
	}
	return []slog.Attr{
		slog.String("environment", a.Config.Environment),
		slog.String("storage", string(a.Config.Storage)),
		slog.String("cache", string(a.Config.Cache)),
		slog.String("events", string(a.Config.Events.Kind)),
		slog.String("aws", awsMode),
		slog.String("llm", a.LLM.Name()),
		slog.Bool("autonomous_execution", a.Config.Features.AutonomousExecution),
	}
}
