// Package app is CloudOptix's composition root: the one place where a
// config.Config is turned into working adapters, application services, an
// HTTP router and a set of background workers.
//
// This is the only package in the codebase allowed to import both
// internal/application and internal/adapters. That is not a stylistic
// preference — it is what makes the dependency rule enforceable everywhere
// else. The rule (nothing under internal/domain or internal/application may
// import internal/adapters; CI checks it, see
// .github/scripts/check-architecture-deps.sh) only survives if there is
// somewhere legitimate for the two sides to meet. Without such a place, the
// pressure to "just import the Postgres repository here, it's only one call"
// lands on whichever application service happens to need something concrete
// first, and the hexagon leaks one import at a time until the ports are
// decorative. Concentrating that knowledge in a single package means the
// check can be a blunt, unarguable grep with one documented exemption, and
// means a reviewer asking "what does this deployment actually run against"
// reads one file instead of tracing constructor calls through fifteen.
//
// The second decision worth stating: Build has a defaults path that needs no
// configuration and no infrastructure at all — memory storage, the simulated
// AWS estate, the deterministic LLM provider, an in-process event bus. That
// path is not a test double bolted on beside the real one; it is the same
// Build, the same services and the same router, differing only in which
// adapter each port resolves to. A demo mode assembled by a separate,
// simplified wiring function would drift from production wiring the first
// time either changed, and the demo would then stop being evidence that the
// platform works.
//
// Traceability: REQ-ARCH-001, REQ-OPS-002, SPEC-ARCH-003, SPEC-OPS-001.
package app
