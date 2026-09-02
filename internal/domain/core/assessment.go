package core

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Provenance records how CloudOptix came to believe a fact. The onboarding
// agent, the discovery engine and the economics engine all populate specs and
// models from a mix of user statements, AWS API responses and inference. The
// product promise is that a user can always see which is which, so provenance
// is a first-class part of the model rather than a logging concern.
//
// Traceability: REQ-ONB-004, SPEC-ONB-002.
type Provenance string

const (
	// ProvenanceConfirmed means a human stated it or an authoritative API
	// returned it. Safe to act on.
	ProvenanceConfirmed Provenance = "CONFIRMED"
	// ProvenanceInferred means CloudOptix derived it from other evidence.
	// Usable for analysis, never sufficient on its own for a production
	// mutation.
	ProvenanceInferred Provenance = "INFERRED"
	// ProvenanceUnknown means the field was asked about and the user did not
	// know, or discovery could not determine it. Engines must degrade
	// gracefully rather than substituting a default.
	ProvenanceUnknown Provenance = "UNKNOWN"
	// ProvenanceRequiresConfirmation means CloudOptix has a value but its
	// consequences are large enough that a human must sign it off before it
	// influences execution.
	ProvenanceRequiresConfirmation Provenance = "REQUIRES_USER_CONFIRMATION"
)

// Valid reports whether the provenance is one of the four known values.
func (p Provenance) Valid() bool {
	switch p {
	case ProvenanceConfirmed, ProvenanceInferred, ProvenanceUnknown, ProvenanceRequiresConfirmation:
		return true
	}
	return false
}

// Actionable reports whether a value with this provenance may drive an
// automated change without further human input.
func (p Provenance) Actionable() bool { return p == ProvenanceConfirmed }

// String satisfies fmt.Stringer.
func (p Provenance) String() string { return string(p) }

// RiskLevel is the coarse risk banding shown to humans. It is always derived
// from a numeric score so that the banding thresholds live in one place.
type RiskLevel string

const (
	RiskNone     RiskLevel = "NONE"
	RiskLow      RiskLevel = "LOW"
	RiskMedium   RiskLevel = "MEDIUM"
	RiskHigh     RiskLevel = "HIGH"
	RiskCritical RiskLevel = "CRITICAL"
)

// Order returns a sortable rank for the level.
func (r RiskLevel) Order() int {
	switch r {
	case RiskNone:
		return 0
	case RiskLow:
		return 1
	case RiskMedium:
		return 2
	case RiskHigh:
		return 3
	case RiskCritical:
		return 4
	}
	return 2
}

// AtLeast reports whether r is at or above the given level.
func (r RiskLevel) AtLeast(other RiskLevel) bool { return r.Order() >= other.Order() }

// RiskLevelFromScore bands a 0..1 risk score. The thresholds are deliberately
// asymmetric: the top band is narrow because CloudOptix would rather send a
// borderline change to a human than execute it.
func RiskLevelFromScore(score float64) RiskLevel {
	switch {
	case score <= 0.05:
		return RiskNone
	case score < 0.25:
		return RiskLow
	case score < 0.55:
		return RiskMedium
	case score < 0.80:
		return RiskHigh
	default:
		return RiskCritical
	}
}

// Confidence is a calibrated 0..1 belief that a recommendation's predicted
// effect will actually occur. It is deliberately NOT an LLM self-report: it is
// computed from metric stability, observation window, dependency completeness
// and the historical accuracy of the rule that produced the recommendation.
//
// Traceability: REQ-OPT-006, SPEC-OPT-004.
type Confidence float64

// Clamp bounds the value into [0,1].
func (c Confidence) Clamp() Confidence {
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// Percent renders the confidence for display.
func (c Confidence) Percent() int { return int(math.Round(float64(c.Clamp()) * 100)) }

// Band groups confidence into the labels used in the UI and in policy rules.
func (c Confidence) Band() string {
	switch v := c.Clamp(); {
	case v >= 0.90:
		return "VERY_HIGH"
	case v >= 0.75:
		return "HIGH"
	case v >= 0.55:
		return "MODERATE"
	case v >= 0.35:
		return "LOW"
	default:
		return "VERY_LOW"
	}
}

// String satisfies fmt.Stringer.
func (c Confidence) String() string { return fmt.Sprintf("%d%%", c.Percent()) }

// Severity ranks findings and notifications.
type Severity string

const (
	SeverityInfo     Severity = "INFO"
	SeverityLow      Severity = "LOW"
	SeverityMedium   Severity = "MEDIUM"
	SeverityHigh     Severity = "HIGH"
	SeverityCritical Severity = "CRITICAL"
)

// Order returns a sortable rank for the severity.
func (s Severity) Order() int {
	switch s {
	case SeverityInfo:
		return 0
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	}
	return 0
}

// Criticality expresses how much the business cares about a workload. It
// multiplies blast radius and divides automation appetite.
type Criticality string

const (
	CriticalityTier0 Criticality = "TIER_0" // revenue-path, customer-facing
	CriticalityTier1 Criticality = "TIER_1" // customer-facing, degradable
	CriticalityTier2 Criticality = "TIER_2" // internal, business hours
	CriticalityTier3 Criticality = "TIER_3" // batch, best effort
	CriticalityUnset Criticality = "UNSET"
)

// Weight converts the tier into the multiplier used by blast radius scoring.
func (c Criticality) Weight() float64 {
	switch c {
	case CriticalityTier0:
		return 1.0
	case CriticalityTier1:
		return 0.75
	case CriticalityTier2:
		return 0.45
	case CriticalityTier3:
		return 0.2
	default:
		return 0.6 // unknown criticality is treated as moderately important
	}
}

// Period is a half-open time interval [Start, End). Every cost figure, metric
// window and savings claim in CloudOptix carries one; a number without a
// window is not a fact.
type Period struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// NewPeriod builds a period, normalising to UTC.
func NewPeriod(start, end time.Time) Period {
	return Period{Start: start.UTC(), End: end.UTC()}
}

// PeriodOfDays returns the period covering the n days ending now.
func PeriodOfDays(now time.Time, days int) Period {
	end := now.UTC()
	return Period{Start: end.AddDate(0, 0, -days), End: end}
}

// MonthOf returns the calendar month containing t.
func MonthOf(t time.Time) Period {
	u := t.UTC()
	start := time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	return Period{Start: start, End: start.AddDate(0, 1, 0)}
}

// Duration returns the length of the period.
func (p Period) Duration() time.Duration { return p.End.Sub(p.Start) }

// Days returns the fractional number of days covered.
func (p Period) Days() float64 { return p.Duration().Hours() / 24 }

// Hours returns the number of hours covered.
func (p Period) Hours() float64 { return p.Duration().Hours() }

// Contains reports whether t falls inside the half-open interval.
func (p Period) Contains(t time.Time) bool {
	u := t.UTC()
	return !u.Before(p.Start) && u.Before(p.End)
}

// Overlaps reports whether two periods intersect.
func (p Period) Overlaps(o Period) bool { return p.Start.Before(o.End) && o.Start.Before(p.End) }

// IsZero reports whether the period is unset.
func (p Period) IsZero() bool { return p.Start.IsZero() && p.End.IsZero() }

// String renders the period for logs.
func (p Period) String() string {
	return fmt.Sprintf("%s..%s", p.Start.Format(time.RFC3339), p.End.Format(time.RFC3339))
}

// Percentiles is a summary of a metric distribution. CloudOptix rightsizes on
// high percentiles rather than averages: an instance averaging 8% CPU that
// touches 95% at P99 during a nightly batch is not a downsizing candidate, and
// averages are exactly how naive tools generate outage-causing advice.
//
// Traceability: REQ-UTL-003, SPEC-UTL-002.
type Percentiles struct {
	Min       float64 `json:"min"`
	P50       float64 `json:"p50"`
	P90       float64 `json:"p90"`
	P95       float64 `json:"p95"`
	P99       float64 `json:"p99"`
	Max       float64 `json:"max"`
	Mean      float64 `json:"mean"`
	StdDev    float64 `json:"std_dev"`
	Samples   int     `json:"samples"`
	Coverage  float64 `json:"coverage"`   // fraction of the window with data
	Stability float64 `json:"stability"`  // 1 - normalized dispersion, 0..1
	Trend     float64 `json:"trend"`      // least-squares slope per day
	Seasonal  bool    `json:"seasonal"`   // a daily or weekly cycle was detected
	PeakHours []int   `json:"peak_hours"` // UTC hours of observed peaks
}

// SummarizeSamples computes the percentile summary for a series. It is the one
// place in the codebase that turns raw metric points into the statistics every
// optimization rule reads, so that two rules never disagree about what P95
// means.
func SummarizeSamples(values []float64, coverage float64) Percentiles {
	if len(values) == 0 {
		return Percentiles{Coverage: coverage}
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	var sum float64
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(len(sorted))

	var variance float64
	for _, v := range sorted {
		d := v - mean
		variance += d * d
	}
	variance /= float64(len(sorted))
	std := math.Sqrt(variance)

	p := Percentiles{
		Min:      sorted[0],
		Max:      sorted[len(sorted)-1],
		P50:      quantile(sorted, 0.50),
		P90:      quantile(sorted, 0.90),
		P95:      quantile(sorted, 0.95),
		P99:      quantile(sorted, 0.99),
		Mean:     mean,
		StdDev:   std,
		Samples:  len(sorted),
		Coverage: coverage,
	}
	// Stability is 1 minus the coefficient of variation, clamped. A flat
	// series scores 1; a series whose standard deviation matches its mean
	// scores 0. Rules multiply confidence by this.
	if mean > 0 {
		p.Stability = math.Max(0, 1-(std/mean))
	} else if std == 0 {
		p.Stability = 1
	}
	return p
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// Headroom reports the proportional gap between a percentile utilisation and
// full capacity, which is the quantity every rightsizing rule reasons about.
func (p Percentiles) Headroom(at float64) float64 {
	return math.Max(0, 1-(at/100.0))
}

// Label describes the distribution shape in words for the copilot.
func (p Percentiles) Label() string {
	switch {
	case p.Samples == 0:
		return "no data"
	case p.Stability > 0.85 && p.P99 < 20:
		return "consistently idle"
	case p.Stability > 0.8:
		return "steady"
	case p.P99 > 4*math.Max(p.P50, 1):
		return "spiky"
	case p.Seasonal:
		return "cyclical"
	default:
		return "variable"
	}
}

// Normalize trims and lowercases a free-text key for stable comparison.
func Normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
