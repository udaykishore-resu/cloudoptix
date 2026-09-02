// Package simulation implements ports.SimulationService: the Architecture
// Mutation Engine, the Counterfactual Engine, and the wiring that lets the
// Cost Compiler (internal/application/compiler) serve the Compile,
// GetCompilation, RunRegression, UpsertRegressionSuite and
// ListRegressionSuites methods that same interface bundles.
//
// The key design decision — why the compiler's engine logic lives in a
// separate, dependency-free package rather than inline here — is that the
// compiler is a pure function of an IaC input and a pricing catalog (no
// persistence, no scope resolution), while the mutation and counterfactual
// engines are inherently about the tenant's stored estate: they load an
// Inventory and Topology, generate or reprice against them, and persist the
// result. Splitting on that seam means the compiler's parsers and pricing
// logic get the same fast, storage-free test suite whether they are called
// through this Service or (as CI tooling might) directly, and this package's
// own tests can focus entirely on scope resolution, candidate generation and
// scenario modelling against fake repositories.
//
// Every number this package produces is a model, not a measurement: an
// architecture candidate's ComponentChange deltas and a counterfactual's
// StateProjection are computed from the pricing catalog against the current
// Inventory, and every input this package could not observe (an assumed
// invocation rate, an assumed cache hit rate, an assumed replica-count
// midpoint) travels as a stated, overridable Assumption — the same
// discipline internal/domain/simulate's package doc commits the whole
// family of engines to.
//
// Traceability: REQ-SIM-001..010, SPEC-SIM-001.
package simulation
