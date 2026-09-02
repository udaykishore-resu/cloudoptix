package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Config configures Server. It mirrors config.ServerConfig field-for-field
// deliberately (see internal/infrastructure/config) rather than taking that
// type directly, so this package has no import-time dependency on config and
// can be unit tested and reused without constructing a full platform
// Config.
type Config struct {
	Addr              string
	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int

	TLSEnabled    bool
	TLSCertFile   string
	TLSKeyFile    string
	TLSMinVersion string // "1.2" | "1.3"
}

func (c Config) withDefaults() Config {
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = 10 * time.Second
	}
	if c.ReadHeaderTimeout <= 0 {
		c.ReadHeaderTimeout = 5 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 30 * time.Second
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 120 * time.Second
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 20 * time.Second
	}
	if c.MaxHeaderBytes <= 0 {
		c.MaxHeaderBytes = 1 << 20 // 1 MiB — generous for headers, not for a request body
	}
	if c.TLSMinVersion == "" {
		c.TLSMinVersion = "1.3"
	}
	return c
}

// Server wraps http.Server with CloudOptix's operational defaults.
type Server struct {
	httpServer *http.Server
	cfg        Config
	logger     *slog.Logger
}

// New builds a Server. handler is the fully-assembled router (chi, with the
// whole middleware chain) — this package knows nothing about routing.
func New(cfg Config, handler http.Handler, logger *slog.Logger) (*Server, error) {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Addr == "" {
		return nil, fmt.Errorf("server: Config.Addr is required")
	}

	hs := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	if cfg.TLSEnabled {
		tlsCfg, err := buildTLSConfig(cfg)
		if err != nil {
			return nil, err
		}
		hs.TLSConfig = tlsCfg
	}

	return &Server{httpServer: hs, cfg: cfg, logger: logger}, nil
}

func buildTLSConfig(cfg Config) (*tls.Config, error) {
	minVersion := uint16(tls.VersionTLS13)
	if cfg.TLSMinVersion == "1.2" {
		minVersion = tls.VersionTLS12
	} else if cfg.TLSMinVersion != "1.3" && cfg.TLSMinVersion != "" {
		return nil, fmt.Errorf("server: unsupported tls_min_version %q (want \"1.2\" or \"1.3\")", cfg.TLSMinVersion)
	}
	return &tls.Config{
		MinVersion: minVersion,
		// A curated cipher suite list is only consulted by Go's TLS stack
		// for TLS 1.2 connections — 1.3 uses its own fixed, already-strong
		// set — but restricting it here still hardens a deployment that sets
		// TLSMinVersion to "1.2" for a legacy client compatibility reason.
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
		PreferServerCipherSuites: true,
	}, nil
}

// ListenAndServe starts the server and blocks until it stops. It returns nil
// on a clean shutdown (triggered by Shutdown) and a non-nil error for any
// other failure — matching http.Server's own ErrServerClosed convention so
// callers write `if err := srv.ListenAndServe(); err != nil { ... }` exactly
// as they would against the stdlib type directly.
func (s *Server) ListenAndServe() error {
	var err error
	if s.cfg.TLSEnabled {
		err = s.httpServer.ListenAndServeTLS(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	} else {
		err = s.httpServer.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Serve is like ListenAndServe but accepts a pre-built listener, which is
// what lets tests bind an ephemeral port (":0") and learn the actual address
// before the server starts accepting.
func (s *Server) Serve(l net.Listener) error {
	var err error
	if s.cfg.TLSEnabled {
		err = s.httpServer.ServeTLS(l, s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	} else {
		err = s.httpServer.Serve(l)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops the server: it stops accepting new connections
// immediately and waits for in-flight requests to finish, up to
// Config.ShutdownTimeout. A request still running when the timeout elapses
// is forcibly cancelled via its context, which is what makes it safe to
// always call Shutdown with a bound rather than risk hanging process
// termination on one stuck handler forever.
func (s *Server) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ShutdownTimeout)
	defer cancel()
	s.logger.Info("server shutting down", slog.Duration("timeout", s.cfg.ShutdownTimeout))
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.logger.Error("graceful shutdown did not complete in time, forcing close", slog.String("error", err.Error()))
		return s.httpServer.Close()
	}
	return nil
}

// Addr returns the address the server is configured to listen on.
func (s *Server) Addr() string { return s.cfg.Addr }
