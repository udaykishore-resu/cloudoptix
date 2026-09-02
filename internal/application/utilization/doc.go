// Package utilization is the statistics engine that turns raw, timestamped
// metric points into the core.Percentiles summaries every optimization rule,
// twin projection and copilot answer reads.
//
// The key design decision: core.SummarizeSamples (in package core) computes
// the distribution statistics — percentiles, mean, stddev, stability — from a
// bare slice of values, because that computation has no notion of time and
// belongs in the domain layer where every package can reach it without an
// import cycle. Trend and seasonality are different: they are inherently
// about *when* a value was observed, not just what it was, so they require
// the timestamped points and the window they were collected over. This
// package owns that half of the computation and hands back the same
// core.Percentiles struct with Trend, Seasonal and PeakHours filled in,
// so a rule reading a ResourceMetrics summary never has to know which layer
// computed which field.
//
// Seasonality is detected with autocorrelation at the 24-hour and 168-hour
// (weekly) lags on an hourly-resampled series, rather than an FFT or a
// fitted model: autocorrelation is the simplest technique that directly
// answers the question a rightsizing rule actually asks — "does yesterday's
// shape predict today's" — and its result (a correlation coefficient) is
// something a human can sanity-check by eye against the graph, which matters
// for a signal that gates a scheduling recommendation.
//
// Traceability: REQ-UTL-001..007, SPEC-UTL-002.
package utilization
