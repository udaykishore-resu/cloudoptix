// Package server wraps net/http.Server with the operational behaviour every
// production CloudOptix deployment needs and no handler should have to
// reimplement: sane timeouts, a modern TLS floor, graceful shutdown that
// waits for in-flight requests, and a health/readiness/liveness triple that
// actually probes the dependencies it claims to check rather than always
// returning 200.
//
// The liveness/readiness split matters operationally: liveness answers "is
// this process wedged and should the orchestrator restart it" (so it must
// never depend on anything external — a database outage must not cause a
// restart storm across every pod), while readiness answers "should the load
// balancer send this pod traffic right now" (so it must depend on exactly
// the things a request needs to succeed). Conflating the two is the single
// most common Kubernetes health-check mistake; this package keeps them as
// separate handlers backed by separate check sets on purpose.
//
// Traceability: REQ-OPS-001 (graceful lifecycle), SPEC-OPS-003.
package server
