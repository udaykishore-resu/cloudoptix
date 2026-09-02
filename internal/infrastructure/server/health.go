package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Check is one dependency probe: a database ping, a Redis ping, an AWS STS
// GetCallerIdentity, an LLM provider health check. It must return quickly —
// callers wrap every check with a short timeout (see CheckWithTimeout) so one
// hanging dependency cannot make the whole readiness probe hang.
type Check func(ctx context.Context) error

// NamedCheck pairs a check with the name it reports under in the readiness
// response body, so an operator staring at a failing probe knows which
// dependency to look at without cross-referencing source code.
type NamedCheck struct {
	Name  string
	Check Check
}

// CheckWithTimeout bounds a check to d, converting a hang into a timeout
// error rather than letting it block the whole readiness handler
// indefinitely.
func CheckWithTimeout(name string, d time.Duration, check Check) NamedCheck {
	return NamedCheck{Name: name, Check: func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- check(ctx) }()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
}

// Health serves the three-endpoint health surface.
type Health struct {
	// Liveness checks must never depend on an external dependency (database,
	// cache, AWS, an upstream API) — see the package doc for why. They exist
	// to catch a genuinely wedged process: a deadlock, an exhausted
	// goroutine pool.
	liveness []NamedCheck
	// Readiness checks are everything a request actually needs to succeed.
	readiness []NamedCheck

	mu          sync.RWMutex
	startedAt   time.Time
	version     string
	serviceName string
}

// NewHealth builds a Health with the given liveness and readiness checks.
func NewHealth(serviceName, version string, liveness, readiness []NamedCheck) *Health {
	return &Health{
		liveness: liveness, readiness: readiness,
		startedAt: time.Now(), version: version, serviceName: serviceName,
	}
}

type checkResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok" | "error"
	Error  string `json:"error,omitempty"`
}

type healthResponse struct {
	Status    string        `json:"status"` // "ok" | "degraded"
	Service   string        `json:"service"`
	Version   string        `json:"version"`
	UptimeSec float64       `json:"uptime_seconds"`
	Checks    []checkResult `json:"checks,omitempty"`
	CheckedAt time.Time     `json:"checked_at"`
}

func runChecks(ctx context.Context, checks []NamedCheck) ([]checkResult, bool) {
	results := make([]checkResult, len(checks))
	healthy := true
	// Checks run concurrently — a readiness probe that serially pings
	// Postgres, Redis and the LLM provider one after another is exactly the
	// kind of thing that turns a fast dependency outage detection into a
	// slow one.
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func(i int, c NamedCheck) {
			defer wg.Done()
			if err := c.Check(ctx); err != nil {
				results[i] = checkResult{Name: c.Name, Status: "error", Error: err.Error()}
			} else {
				results[i] = checkResult{Name: c.Name, Status: "ok"}
			}
		}(i, c)
	}
	wg.Wait()
	for _, r := range results {
		if r.Status != "ok" {
			healthy = false
		}
	}
	return results, healthy
}

// LivenessHandler serves /healthz: process-internal checks only. It answers
// "should the orchestrator restart this pod" and must stay cheap and free of
// external dependencies.
func (h *Health) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		results, healthy := runChecks(r.Context(), h.liveness)
		h.write(w, results, healthy)
	}
}

// ReadinessHandler serves /readyz: every dependency a request needs. It
// answers "should the load balancer send this pod traffic right now".
func (h *Health) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		results, healthy := runChecks(r.Context(), h.readiness)
		h.write(w, results, healthy)
	}
}

// HealthHandler serves a combined, human-facing summary (both liveness and
// readiness checks) for a dashboard or an operator running curl by hand —
// distinct from the two machine-consumed probes above so that adding a
// convenience endpoint never changes what Kubernetes actually polls.
func (h *Health) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		all := append(append([]NamedCheck{}, h.liveness...), h.readiness...)
		results, healthy := runChecks(r.Context(), all)
		h.write(w, results, healthy)
	}
}

func (h *Health) write(w http.ResponseWriter, results []checkResult, healthy bool) {
	status := "ok"
	code := http.StatusOK
	if !healthy {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}
	resp := healthResponse{
		Status: status, Service: h.serviceName, Version: h.version,
		UptimeSec: time.Since(h.startedAt).Seconds(), Checks: results, CheckedAt: time.Now().UTC(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(resp)
}
