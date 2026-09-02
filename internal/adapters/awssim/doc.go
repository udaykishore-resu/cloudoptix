// Package awssim is a deterministic, in-repo simulation of an AWS account.
//
// The design decision this package turns on: the demo tenant, the entire
// integration test suite and CI run against this package instead of a real
// AWS account. That is only trustworthy if awssim behaves like the ports it
// implements in every way that matters to the engines built on top of
// them — a discovered resource must cost what the cost ingestor bills for
// it, a metric profile declared "spiky" must actually produce a spiky
// series, and an executed mutation must actually change what the estate
// bills next. A simulator that merely returns plausible-looking data would
// let a whole class of bugs (a rule that reads the wrong percentile, a
// validator that can't tell a real improvement from noise) pass every test
// and then fail on the first real customer. So every adapter in this
// package reads and writes the same in-memory Estate, and nothing here is a
// stub: Discover walks real attachment state, Fetch bills real hourly
// rates, Collect samples real declared distributions, and Apply performs a
// real, reversible mutation of the estate.
//
// Traceability: REQ-SIM-001..009, SPEC-DEMO-001 (demo tenant fidelity).
package awssim
