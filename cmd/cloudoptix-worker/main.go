// Command cloudoptix-worker runs CloudOptix's background cycles.
//
// Which cycles a process runs is a deployment decision, not a code one:
// --workers=discovery,cost is a pod that scans estates and reprices them,
// --workers=automation is a pod with the execute role, and the two are
// scaled and permissioned independently. That is why the flag exists rather
// than every replica running everything — a discovery pod that never
// executes anything does not need an execute role at all, and the smallest
// blast radius available is the one where it cannot have one.
//
// Traceability: REQ-OPS-001, REQ-AUTO-004, SPEC-OPS-001.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/app"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/telemetry"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cloudoptix-worker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("cloudoptix-worker", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "path to a YAML configuration file (optional)")
	workers := fs.String("workers", "all",
		"comma-separated cycles to run: "+strings.Join(app.AllWorkers(), ",")+" (default all)")
	seedDemo := fs.Bool("seed-demo", false, "seed the demo tenant before starting (requires aws.mode=simulated); idempotent")
	once := fs.Bool("once", false, "run each selected cycle exactly once and exit, instead of looping")
	showVersion := fs.Bool("version", false, "print version information and exit")

	own, rest := app.SplitArgs(fs, os.Args[1:])
	if err := fs.Parse(own); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parsing flags: %w", err)
	}

	if *showVersion {
		fmt.Printf("cloudoptix-worker %s (commit %s, built %s)\n", version, commit, buildDate)
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
	if cfg.Telemetry.ServiceName == "" || cfg.Telemetry.ServiceName == "cloudoptix-api" {
		cfg.Telemetry.ServiceName = "cloudoptix-worker"
	}
	if cfg.Telemetry.ServiceVersion == "dev" || cfg.Telemetry.ServiceVersion == "" {
		cfg.Telemetry.ServiceVersion = version
	}

	// A worker fleet larger than one replica needs a shared lock table or
	// ports.Locker's mutual-exclusion contract is not met — two replicas would
	// each hold their own in-process lock and both execute the same plan. The
	// process cannot see its own replica count, so this warns rather than
	// refuses; the deployment (helm/cloudoptix) is where the two are actually
	// reconciled.
	logger := telemetry.NewLogger(telemetry.LogConfig{
		Level: cfg.Telemetry.LogLevel, Format: cfg.Telemetry.LogFormat,
	})
	slog.SetDefault(logger)
	if cfg.Cache == config.CacheMemory {
		logger.Warn("cache=memory: locks are in-process only, so this worker must run as a single replica; " +
			"set cache=redis before scaling past one")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tp := telemetry.NewTracerProvider(telemetry.TracerConfig{
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

	selected, err := application.SelectWorkers(*workers)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(selected))
	for _, w := range selected {
		names = append(names, w.Name)
	}

	logger.LogAttrs(ctx, slog.LevelInfo, "cloudoptix-worker starting",
		append([]slog.Attr{
			slog.String("version", version),
			slog.String("commit", commit),
			slog.String("workers", strings.Join(names, ",")),
		}, application.Describe()...)...)

	if *seedDemo {
		result, err := app.Seed(ctx, application)
		if err != nil {
			return fmt.Errorf("seeding the demo tenant: %w", err)
		}
		logger.Info("demo tenant ready",
			slog.String("tenant", string(result.TenantID)),
			slog.Bool("already_seeded", result.AlreadyRan))
	}

	if *once {
		// --once is what a Kubernetes CronJob and a test use: run the cycle,
		// report what happened, exit with the cycle's own status. A looping
		// worker never returns an error (a failed cycle is retried next
		// tick), so this is the only mode where a cycle failure can be
		// surfaced as a process exit code.
		var failed []string
		for _, w := range selected {
			if err := w.RunOnce(ctx); err != nil {
				logger.Error("cycle failed", slog.String("worker", w.Name), slog.String("error", err.Error()))
				failed = append(failed, w.Name)
				continue
			}
			logger.Info("cycle complete", slog.String("worker", w.Name))
		}
		if len(failed) > 0 {
			return fmt.Errorf("%d cycle(s) failed: %s", len(failed), strings.Join(failed, ", "))
		}
		return nil
	}

	app.RunWorkers(ctx, selected)
	logger.Info("all workers drained, shutting down")
	return nil
}
