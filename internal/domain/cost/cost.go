// Package cost models billed spend: the line items CloudOptix ingests from
// Cost Explorer and the Cost & Usage Report, the roll-ups computed from them,
// and the forecasting and anomaly primitives built on top.
//
// The distinction this package guards is between *billed* cost, which is a
// fact AWS asserts, and *attributed* cost, which is CloudOptix's model of who
// caused it. Billed cost lives here. Attribution lives in package econ. Mixing
// them is how FinOps tools end up reporting numbers that do not reconcile with
// the invoice.
//
// Traceability: REQ-COST-001..008, SPEC-COST-001.
package cost

import (
	"math"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Granularity is the time bucket of a cost series.
type Granularity string

const (
	GranularityHourly  Granularity = "hourly"
	GranularityDaily   Granularity = "daily"
	GranularityMonthly Granularity = "monthly"
)

// AmortizationBasis selects which of AWS's several cost figures a record
// carries. CloudOptix stores amortized cost as the primary basis, because
// unblended cost makes a Savings Plan purchase look like a one-off spike and
// then makes the following eleven months look artificially cheap — which
// produces nonsense recommendations.
type AmortizationBasis string

const (
	BasisAmortized    AmortizationBasis = "amortized"
	BasisUnblended    AmortizationBasis = "unblended"
	BasisNetAmortized AmortizationBasis = "net_amortized"
	BasisBlended      AmortizationBasis = "blended"
)

// ChargeType distinguishes usage from the accounting entries that surround it.
// Credits and refunds must not be spread across workloads as if they were
// consumption, or a team's cost-per-transaction moves when finance applies a
// credit that has nothing to do with them.
type ChargeType string

const (
	ChargeUsage                ChargeType = "Usage"
	ChargeTax                  ChargeType = "Tax"
	ChargeCredit               ChargeType = "Credit"
	ChargeRefund               ChargeType = "Refund"
	ChargeFee                  ChargeType = "Fee"
	ChargeSavingsPlanRecurring ChargeType = "SavingsPlanRecurringFee"
	ChargeRIFee                ChargeType = "RIFee"
	ChargeDiscount             ChargeType = "Discount"
)

// Attributable reports whether a charge type participates in workload cost
// attribution.
func (c ChargeType) Attributable() bool {
	switch c {
	case ChargeUsage, ChargeSavingsPlanRecurring, ChargeRIFee:
		return true
	}
	return false
}

// Record is one normalized cost line. It is the atom of the cost engine: every
// dashboard number, every SLO evaluation and every savings claim reduces to a
// sum over these.
type Record struct {
	ID        core.ID        `json:"id"`
	TenantID  core.TenantID  `json:"tenant_id"`
	AccountID core.AccountID `json:"account_id"`
	Region    core.Region    `json:"region,omitempty"`
	AZ        string         `json:"availability_zone,omitempty"`

	Period      core.Period `json:"period"`
	Granularity Granularity `json:"granularity"`

	// Service is the Cost Explorer SERVICE dimension ("Amazon Elastic Compute
	// Cloud - Compute"). UsageType and Operation are what make a line
	// actionable: "NatGateway-Bytes" is a different optimization problem from
	// "NatGateway-Hours" even though both are "Amazon Virtual Private Cloud".
	Service   string `json:"service"`
	UsageType string `json:"usage_type,omitempty"`
	Operation string `json:"operation,omitempty"`

	ResourceID  core.ID  `json:"resource_id,omitempty"` // resolved CloudOptix resource
	ResourceARN core.ARN `json:"resource_arn,omitempty"`

	ChargeType ChargeType        `json:"charge_type"`
	Basis      AmortizationBasis `json:"basis"`
	Amount     core.Money        `json:"amount"`
	UsageQty   float64           `json:"usage_quantity,omitempty"`
	UsageUnit  string            `json:"usage_unit,omitempty"`

	Tags        core.Tags        `json:"tags,omitempty"`
	Environment core.Environment `json:"environment,omitempty"`
	Source      string           `json:"source"` // "cost_explorer" | "cur" | "simulator"
	IngestedAt  time.Time        `json:"ingested_at"`
}

// Series is a time-ordered cost series with the statistics the trend, forecast
// and anomaly engines need.
type Series struct {
	Granularity Granularity   `json:"granularity"`
	Points      []Point       `json:"points"`
	Currency    core.Currency `json:"currency"`
}

// Point is one bucket of a cost series.
type Point struct {
	Period core.Period `json:"period"`
	Amount core.Money  `json:"amount"`
}

// Total sums the series.
func (s Series) Total() core.Money {
	total := core.ZeroUSD()
	for _, p := range s.Points {
		total = total.MustAdd(p.Amount)
	}
	return total
}

// Mean returns the average bucket value.
func (s Series) Mean() core.Money {
	if len(s.Points) == 0 {
		return core.ZeroUSD()
	}
	return s.Total().Div(float64(len(s.Points)))
}

// Values projects the series onto floats for statistical work.
func (s Series) Values() []float64 {
	out := make([]float64, len(s.Points))
	for i, p := range s.Points {
		out[i] = p.Amount.Units()
	}
	return out
}

// Sorted returns a copy ordered by period start.
func (s Series) Sorted() Series {
	pts := make([]Point, len(s.Points))
	copy(pts, s.Points)
	sort.Slice(pts, func(i, j int) bool { return pts[i].Period.Start.Before(pts[j].Period.Start) })
	return Series{Granularity: s.Granularity, Points: pts, Currency: s.Currency}
}

// Trend fits a least-squares line to the series and reports the slope per
// bucket together with the coefficient of determination. A high R² is what
// separates a real cost trend from noise, and the forecast refuses to
// extrapolate a trend it cannot fit.
func (s Series) Trend() (slopePerBucket float64, r2 float64) {
	n := float64(len(s.Points))
	if n < 3 {
		return 0, 0
	}
	vals := s.Sorted().Values()
	var sumX, sumY, sumXY, sumXX float64
	for i, y := range vals {
		x := float64(i)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := n*sumXX - sumX*sumX
	if denom == 0 {
		return 0, 0
	}
	slope := (n*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / n

	meanY := sumY / n
	var ssTot, ssRes float64
	for i, y := range vals {
		pred := intercept + slope*float64(i)
		ssRes += (y - pred) * (y - pred)
		ssTot += (y - meanY) * (y - meanY)
	}
	if ssTot == 0 {
		return slope, 1
	}
	return slope, math.Max(0, 1-(ssRes/ssTot))
}

// Forecast is a projection with an explicit uncertainty band and an explicit
// method. CloudOptix never presents a bare number: a forecast whose band spans
// 40% is a different business input from one whose band spans 4%.
type Forecast struct {
	Period      core.Period     `json:"period"`
	Expected    core.Money      `json:"expected"`
	Low         core.Money      `json:"low"`
	High        core.Money      `json:"high"`
	Method      ForecastMethod  `json:"method"`
	Confidence  core.Confidence `json:"confidence"`
	BasedOnDays int             `json:"based_on_days"`
	Note        string          `json:"note,omitempty"`
}

// ForecastMethod names the projection technique used, so the copilot can
// explain itself.
type ForecastMethod string

const (
	// ForecastRunRate extrapolates the trailing mean. Used when the trend fit
	// is poor — a flat but noisy series.
	ForecastRunRate ForecastMethod = "run_rate"
	// ForecastLinearTrend extrapolates a fitted trend. Used when R² clears
	// the threshold.
	ForecastLinearTrend ForecastMethod = "linear_trend"
	// ForecastSeasonalNaive repeats the equivalent prior period. Used when a
	// weekly cycle dominates, which is common for business applications.
	ForecastSeasonalNaive ForecastMethod = "seasonal_naive"
	// ForecastMonthToDate completes the current month from observed spend.
	ForecastMonthToDate ForecastMethod = "month_to_date"
	// ForecastInsufficient marks a refusal to forecast.
	ForecastInsufficient ForecastMethod = "insufficient_data"
)

// Anomaly is a detected deviation in spend. Detection is deterministic
// (robust z-score over a trailing window) rather than model-based, so the same
// input always produces the same alert and an on-call engineer can reproduce
// it by hand.
type Anomaly struct {
	ID          core.ID       `json:"id"`
	TenantID    core.TenantID `json:"tenant_id"`
	DetectedAt  time.Time     `json:"detected_at"`
	Period      core.Period   `json:"period"`
	Dimension   string        `json:"dimension"` // "service" | "account" | "usage_type" | "application"
	Key         string        `json:"key"`
	Expected    core.Money    `json:"expected"`
	Actual      core.Money    `json:"actual"`
	Delta       core.Money    `json:"delta"`
	DeltaPct    float64       `json:"delta_pct"`
	Score       float64       `json:"score"` // robust z-score
	Severity    core.Severity `json:"severity"`
	Explanation string        `json:"explanation,omitempty"`
	// Contributors ranks the sub-dimensions responsible, which is the answer
	// to "why did cost increase" rather than merely "cost increased".
	Contributors []Contribution `json:"contributors,omitempty"`
	Acknowledged bool           `json:"acknowledged"`
}

// Contribution is one component of a cost movement.
type Contribution struct {
	Dimension string     `json:"dimension"`
	Key       string     `json:"key"`
	Delta     core.Money `json:"delta"`
	Share     float64    `json:"share"` // fraction of the total movement
	Note      string     `json:"note,omitempty"`
}

// Breakdown is a grouped cost roll-up, the shape every dashboard panel and
// copilot answer consumes.
type Breakdown struct {
	Dimension string          `json:"dimension"`
	Period    core.Period     `json:"period"`
	Total     core.Money      `json:"total"`
	Items     []BreakdownItem `json:"items"`
}

// BreakdownItem is one group in a roll-up.
type BreakdownItem struct {
	Key           string     `json:"key"`
	Label         string     `json:"label,omitempty"`
	Amount        core.Money `json:"amount"`
	Share         float64    `json:"share"`
	PriorAmount   core.Money `json:"prior_amount,omitempty"`
	ChangePct     float64    `json:"change_pct,omitempty"`
	ResourceCount int        `json:"resource_count,omitempty"`
}

// NewBreakdown builds a roll-up from a map, computing shares and sorting
// descending by amount.
func NewBreakdown(dimension string, period core.Period, amounts map[string]core.Money) Breakdown {
	b := Breakdown{Dimension: dimension, Period: period, Total: core.ZeroUSD()}
	for _, v := range amounts {
		b.Total = b.Total.MustAdd(v)
	}
	for k, v := range amounts {
		item := BreakdownItem{Key: k, Amount: v}
		if !b.Total.IsZero() {
			item.Share = v.Ratio(b.Total)
		}
		b.Items = append(b.Items, item)
	}
	sort.Slice(b.Items, func(i, j int) bool {
		if b.Items[i].Amount.Micros() != b.Items[j].Amount.Micros() {
			return b.Items[i].Amount.Micros() > b.Items[j].Amount.Micros()
		}
		return b.Items[i].Key < b.Items[j].Key
	})
	return b
}

// TopN truncates a breakdown, folding the remainder into an "Other" row so the
// displayed total still reconciles with the real total.
func (b Breakdown) TopN(n int) Breakdown {
	if len(b.Items) <= n {
		return b
	}
	out := Breakdown{Dimension: b.Dimension, Period: b.Period, Total: b.Total}
	out.Items = append(out.Items, b.Items[:n]...)
	other := core.ZeroUSD()
	count := 0
	for _, it := range b.Items[n:] {
		other = other.MustAdd(it.Amount)
		count += it.ResourceCount
	}
	share := 0.0
	if !b.Total.IsZero() {
		share = other.Ratio(b.Total)
	}
	out.Items = append(out.Items, BreakdownItem{
		Key: "__other__", Label: "Other", Amount: other, Share: share, ResourceCount: count,
	})
	return out
}

// Budget is a spend ceiling with alert thresholds, mirroring AWS Budgets but
// evaluated inside CloudOptix so it can gate changes rather than only notify.
type Budget struct {
	ID         core.ID       `json:"id"`
	TenantID   core.TenantID `json:"tenant_id"`
	Name       string        `json:"name"`
	Scope      BudgetScope   `json:"scope"`
	Amount     core.Money    `json:"amount"`
	Period     Granularity   `json:"period"`
	Thresholds []float64     `json:"thresholds"` // 0.5, 0.8, 1.0
	CreatedAt  time.Time     `json:"created_at"`
}

// BudgetScope narrows a budget to part of the estate.
type BudgetScope struct {
	AccountIDs     []core.AccountID   `json:"account_ids,omitempty"`
	Environments   []core.Environment `json:"environments,omitempty"`
	ApplicationIDs []core.ID          `json:"application_ids,omitempty"`
	Services       []string           `json:"services,omitempty"`
	TagFilters     map[string]string  `json:"tag_filters,omitempty"`
}
