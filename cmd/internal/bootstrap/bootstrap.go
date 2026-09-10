// Package bootstrap is the process setup the three binaries share: load the
// configuration, resolve its secrets, build the logger and tracer, and
// hand back what internal/app.Build needs.
//
// It lives under cmd/ rather than in internal/app because it deals in
// process-level concerns — os.Environ, os.LookupEnv, stderr, ldflags-stamped
// version strings — that the composition root deliberately does not touch,
// which is what keeps app.Build usable from a test that passes a
// config.Config it built by hand.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/udaykishore-resu/cloudoptix/internal/app"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/server"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/telemetry"
)

// BuildInfo is what the Dockerfile's -ldflags stamp into each main package.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// String renders the build information the way `coptx version` prints it.
func (b BuildInfo) String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", b.Version, b.Commit, b.BuildDate)
}

// Options parameterises Load.
type Options struct {
	// ServiceName is the telemetry service name this binary reports when the
	// configuration leaves it at the default (cloudoptix-api).
	ServiceName string
	// ConfigFile is the YAML file layered over the defaults; empty skips it.
	ConfigFile string
	// Args are the configuration flags (-server-port, -storage, ...) left
	// over after the binary parsed its own; see app.SplitArgs.
	Args  []string
	Build BuildInfo
}

// Process is a loaded, not yet built, CloudOptix process.
type Process struct {
	Config *config.Config
	Logger *slog.Logger
	tracer *sdktrace.TracerProvider
}

// Load reads the configuration (defaults, file, environment, flags), resolves
// every secret it references, and constructs the logger and tracer.
func Load(ctx context.Context, opts Options) (*Process, error) {
	args := opts.Args
	if args == nil {
		args = []string{}
	}
	cfg, err := config.Load(config.LoadOptions{
		FilePath: opts.ConfigFile,
		Environ:  os.Environ(),
		Args:     args,
	})
	if err != nil {
		return nil, err
	}

	defaults := config.Defaults()
	if opts.ServiceName != "" && cfg.Telemetry.ServiceName == defaults.Telemetry.ServiceName {
		cfg.Telemetry.ServiceName = opts.ServiceName
	}
	if opts.Build.Version != "" && cfg.Telemetry.ServiceVersion == defaults.Telemetry.ServiceVersion {
		cfg.Telemetry.ServiceVersion = opts.Build.Version
	}

	// There is no secretref: resolver wired yet — a reference of that form
	// fails here with a message naming the field, rather than later with an
	// empty password at connection time.
	if err := config.ResolveSecrets(ctx, cfg, os.LookupEnv, nil); err != nil {
		return nil, err
	}

	logger := telemetry.NewLogger(telemetry.LogConfig{
		Level:  cfg.Telemetry.LogLevel,
		Format: cfg.Telemetry.LogFormat,
		Output: os.Stderr,
	})
	slog.SetDefault(logger)

	p := &Process{Config: cfg, Logger: logger}
	if cfg.Telemetry.TracingEnabled {
		p.tracer = telemetry.NewTracerProvider(telemetry.TracerConfig{
			ServiceName:    cfg.Telemetry.ServiceName,
			ServiceVersion: cfg.Telemetry.ServiceVersion,
			Environment:    cfg.Environment,
			SampleRatio:    cfg.Telemetry.TraceSampleRatio,
			Logger:         logger,
		})
	}
	return p, nil
}

// Build runs the composition root against the loaded configuration.
func (p *Process) Build(ctx context.Context) (*app.App, error) {
	return app.Build(ctx, p.Config, p.Logger)
}

// ServerConfig maps the loaded configuration onto the HTTP server's own.
func (p *Process) ServerConfig() server.Config {
	s := p.Config.Server
	return server.Config{
		Addr:              s.Host + ":" + strconv.Itoa(s.Port),
		ReadTimeout:       s.ReadTimeout,
		ReadHeaderTimeout: s.ReadHeaderTimeout,
		WriteTimeout:      s.WriteTimeout,
		IdleTimeout:       s.IdleTimeout,
		ShutdownTimeout:   s.ShutdownTimeout,
		TLSEnabled:        s.TLSEnabled,
		TLSCertFile:       s.TLSCertFile,
		TLSKeyFile:        s.TLSKeyFile,
		TLSMinVersion:     s.TLSMinVersion,
	}
}

// Shutdown flushes telemetry. It is bounded by the server's shutdown timeout
// so a stuck exporter cannot hold the process open.
func (p *Process) Shutdown() {
	if p.tracer == nil {
		return
	}
	timeout := p.Config.Server.ShutdownTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := telemetry.Shutdown(ctx, p.tracer); err != nil {
		p.Logger.Warn("tracer shutdown", slog.String("error", err.Error()))
	}
}
