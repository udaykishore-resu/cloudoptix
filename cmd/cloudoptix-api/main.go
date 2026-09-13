// Command cloudoptix-api serves the CloudOptix HTTP API.
//
// It does one job: load configuration, build the application (internal/app),
// serve HTTP until signalled, then shut down gracefully. It runs no
// background workers — that is cmd/cloudoptix-worker — because the two have
// genuinely different operational shapes: the API is scaled on request
// latency and must drain in seconds, workers are scaled on tenant count and
// must drain a mid-flight cloud mutation. Running both in one process would
// force one shutdown budget on both.
//
// Traceability: REQ-OPS-001, SPEC-OPS-001.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/app"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/server"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/telemetry"
)

// Build metadata, stamped by the linker (see deployments/docker/Dockerfile).
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	if err := run(); err != nil {
		// stderr, not the structured logger: a configuration error happens
		// before the logger's own configuration is known to be valid, and an
		// operator staring at a crash-looping pod needs a plain sentence.
		fmt.Fprintf(os.Stderr, "cloudoptix-api: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("cloudoptix-api", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "path to a YAML configuration file (optional; environment alone is sufficient)")
	seedDemo := fs.Bool("seed-demo", false, "seed the demo tenant on boot (requires aws.mode=simulated); idempotent")
	migrateOnly := fs.Bool("migrate-only", false,
		"apply pending database migrations and exit without listening (for a pre-deploy hook)")
	showVersion := fs.Bool("version", false, "print version information and exit")

	// config binds its own flag for every knob it supports, so this binary's
	// flags and config's have to be separated before either FlagSet parses.
	own, rest := app.SplitArgs(fs, os.Args[1:])
	if err := fs.Parse(own); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parsing flags: %w", err)
	}

	if *showVersion {
		fmt.Printf("cloudoptix-api %s (commit %s, built %s)\n", version, commit, buildDate)
		return nil
	}

	cfg, err := config.Load(config.LoadOptions{
		FilePath: *configPath, Environ: os.Environ(), Args: rest,
	})
	if err != nil {
		return err
	}
	if err := config.ResolveSecrets(context.Background(), cfg, os.LookupEnv, nil); err != nil {
		return fmt.Errorf("resolving secrets: %w", err)
	}
	if cfg.Telemetry.ServiceName == "" {
		cfg.Telemetry.ServiceName = "cloudoptix-api"
	}
	if cfg.Telemetry.ServiceVersion == "dev" || cfg.Telemetry.ServiceVersion == "" {
		cfg.Telemetry.ServiceVersion = version
	}

	logger := telemetry.NewLogger(telemetry.LogConfig{
		Level: cfg.Telemetry.LogLevel, Format: cfg.Telemetry.LogFormat,
	})
	slog.SetDefault(logger)

	// SIGINT/SIGTERM cancel this context, which is what every shutdown path
	// below hangs off. NotifyContext (rather than a hand-rolled signal
	// channel) also restores the default disposition on stop, so a second
	// signal during a slow drain terminates the process rather than being
	// swallowed — the behaviour an operator expects when they press Ctrl-C
	// twice.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var tp = telemetry.NewTracerProvider(telemetry.TracerConfig{
		ServiceName: cfg.Telemetry.ServiceName, ServiceVersion: cfg.Telemetry.ServiceVersion,
		Environment: cfg.Environment, SampleRatio: cfg.Telemetry.TraceSampleRatio, Logger: logger,
	})
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = telemetry.Shutdown(shutdownCtx, tp)
	}()

	application, err := app.Build(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = application.Close() }()

	if *migrateOnly {
		// Build already applied the migrations (see internal/app/storage.go)
		// — that is the point of this flag rather than a separate migrate
		// binary: a pre-deploy hook that ran a *different* implementation of
		// the schema step could succeed while the API still starts against a
		// schema it does not recognise. Here the hook and the server run the
		// same code, so a green hook means the server's own check will pass.
		logger.LogAttrs(ctx, slog.LevelInfo, "migrations applied; exiting without listening",
			application.Describe()...)
		return nil
	}

	logger.LogAttrs(ctx, slog.LevelInfo, "cloudoptix-api starting",
		append([]slog.Attr{
			slog.String("version", version),
			slog.String("commit", commit),
			slog.String("addr", cfg.Server.Addr()),
			slog.Bool("tls", cfg.Server.TLSEnabled),
		}, application.Describe()...)...)

	if *seedDemo {
		result, err := app.Seed(ctx, application)
		if err != nil {
			return fmt.Errorf("seeding the demo tenant: %w", err)
		}
		logger.Info("demo tenant ready",
			slog.String("tenant", string(result.TenantID)),
			slog.Bool("already_seeded", result.AlreadyRan),
			slog.Int("resources", result.ResourcesDiscovered),
			slog.Int("recommendations", result.Recommendations),
			slog.String("identified_waste", result.MonthlySaving.String()))
		result.PrintSummary(os.Stdout)
	}

	srv, err := server.New(server.Config{
		Addr:              cfg.Server.Addr(),
		ReadTimeout:       cfg.Server.ReadTimeout,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		ShutdownTimeout:   cfg.Server.ShutdownTimeout,
		TLSEnabled:        cfg.Server.TLSEnabled,
		TLSCertFile:       cfg.Server.TLSCertFile,
		TLSKeyFile:        cfg.Server.TLSKeyFile,
		TLSMinVersion:     cfg.Server.TLSMinVersion,
	}, application.Router, logger)
	if err != nil {
		return err
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	logger.Info("listening", slog.String("addr", cfg.Server.Addr()),
		slog.String("health", "/healthz"), slog.String("ready", "/readyz"),
		slog.String("metrics", cfg.Telemetry.MetricsPath))

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		// Stop intercepting signals first, so a second Ctrl-C during the
		// drain kills the process immediately instead of being absorbed.
		stop()
		logger.Info("shutdown signal received, draining",
			slog.Duration("timeout", cfg.Server.ShutdownTimeout))
		if err := srv.Shutdown(context.Background()); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		if err := <-serveErr; err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		logger.Info("shutdown complete")
		return nil
	}
}
