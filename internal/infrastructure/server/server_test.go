package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startTestServer(t *testing.T, cfg Config, handler http.Handler) (*Server, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cfg.Addr = l.Addr().String()

	srv, err := New(cfg, handler, nil)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		select {
		case err := <-errCh:
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("server did not stop")
		}
	})
	return srv, "http://" + l.Addr().String()
}

func TestServer_ServesRequests(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	_, addr := startTestServer(t, Config{}, handler)

	resp, err := http.Get(addr)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "ok", string(body))
}

func TestServer_GracefulShutdownWaitsForInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv, err := New(Config{Addr: l.Addr().String(), ShutdownTimeout: 5 * time.Second}, handler, nil)
	require.NoError(t, err)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(l) }()

	reqDone := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get("http://" + l.Addr().String())
		require.NoError(t, err)
		reqDone <- resp
	}()

	<-started // request is now in flight, blocked on release

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	// Shutdown must not complete while the handler is still blocked.
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned before the in-flight request finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(release) // let the handler finish

	select {
	case err := <-shutdownDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not complete after the request finished")
	}

	resp := <-reqDone
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, <-serveErr)
}

func TestServer_ShutdownTimesOutOnStuckRequest(t *testing.T) {
	started := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done() // never responds on its own; only the forced Close ends it
	})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv, err := New(Config{Addr: l.Addr().String(), ShutdownTimeout: 200 * time.Millisecond}, handler, nil)
	require.NoError(t, err)

	go func() { _ = srv.Serve(l) }()
	go func() { _, _ = http.Get("http://" + l.Addr().String()) }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err = srv.Shutdown(ctx)
	elapsed := time.Since(start)

	// Shutdown must return (via forced Close) at roughly ShutdownTimeout,
	// not hang forever waiting for a handler that never finishes on its own.
	assert.Less(t, elapsed, 2*time.Second)
	_ = err // Close() after a timed-out graceful Shutdown may or may not error depending on timing; only the bound matters here.
}

func TestNew_RequiresAddr(t *testing.T) {
	_, err := New(Config{}, http.NotFoundHandler(), nil)
	require.Error(t, err)
}

func TestBuildTLSConfig_RejectsUnknownVersion(t *testing.T) {
	_, err := buildTLSConfig(Config{TLSMinVersion: "1.0"})
	require.Error(t, err)
}

func TestBuildTLSConfig_DefaultsToTLS13(t *testing.T) {
	cfg, err := buildTLSConfig(Config{})
	require.NoError(t, err)
	assert.Equal(t, uint16(0x0304), cfg.MinVersion) // tls.VersionTLS13
}

func TestListenAndServe_ReturnsNilOnCleanShutdown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv, err := New(Config{Addr: l.Addr().String()}, http.NotFoundHandler(), nil)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))

	select {
	case err := <-done:
		assert.NoError(t, err, "Serve must return nil, not http.ErrServerClosed, on a clean shutdown")
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return")
	}
}
