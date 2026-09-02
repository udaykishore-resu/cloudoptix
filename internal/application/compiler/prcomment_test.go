package compiler

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

func TestRenderPRComment_IncludesEveryRequiredSection(t *testing.T) {
	result := baseResult()
	result.BaselineMonthly = core.USDollars(500)
	result.ProjectedMonthly = core.USDollars(650)
	result.MonthlyDelta = core.USDollars(150)
	result.AnnualDelta = core.USDollars(1800)
	result.DeltaPct = 30
	result.Risks = []simulate.CostRisk{{
		Code: "nat_gateway_fanout", Severity: core.SeverityHigh, Summary: "2 NAT gateways added",
		MonthlyImpact: core.USDollars(90), Remediation: "confirm each AZ needs its own NAT gateway",
	}}
	result.Opportunities = []simulate.Opportunity{{
		Address: "aws_ebs_volume.data", Summary: "gp2 volume declared; gp3 is cheaper",
		MonthlySaving: core.USDollars(12), Change: "switch volume_type to gp3",
	}}

	suite := simulate.RegressionSuite{Name: "default", Checks: []simulate.RegressionCheck{
		{Name: "max-increase", Kind: simulate.CheckMaxMonthlyIncreasePct, Threshold: 10, OnViolation: simulate.VerdictFail},
	}}
	report := EvaluateRegression("t1", "cmp_1", suite, result)

	out := RenderPRComment(result, &report)

	assert.Contains(t, out, "Verdict: FAIL")
	assert.Contains(t, out, "Architecture review required.")
	assert.Contains(t, out, "$500")
	assert.Contains(t, out, "$650")
	assert.Contains(t, out, "+30.0%")
	assert.Contains(t, out, "Top movers")
	assert.Contains(t, out, "aws_instance.api")
	assert.Contains(t, out, "Risks")
	assert.Contains(t, out, "nat_gateway_fanout")
	assert.Contains(t, out, "confirm each AZ needs its own NAT gateway")
	assert.Contains(t, out, "Opportunities")
	assert.Contains(t, out, "gp2 volume declared")
	assert.Contains(t, out, "switch volume_type to gp3")
	assert.Contains(t, out, "Cost regression checks")
	assert.Contains(t, out, "max-increase")

	// The tag-warning sentinel is a purely internal bookkeeping device and
	// must never leak into human-facing output.
	assert.NotContains(t, out, "tags:")
}

func TestRenderPRComment_NoRegressionReportOmitsVerdictSection(t *testing.T) {
	result := baseResult()
	result.MonthlyDelta = core.USDollars(150)
	result.DeltaPct = 30
	out := RenderPRComment(result, nil)
	assert.NotContains(t, out, "Verdict:")
	assert.Contains(t, out, "Monthly cost would increase")
}

func TestRenderPRComment_UnpricedResourcesListed(t *testing.T) {
	result := baseResult()
	result.Changes = append(result.Changes, simulate.PricedChange{
		Address: "aws_msk_cluster.events", Unpriced: true, UnpricedReason: "no pricing data",
	})
	out := RenderPRComment(result, nil)
	assert.Contains(t, out, "Unpriced resources")
	assert.Contains(t, out, "aws_msk_cluster.events")
	assert.Contains(t, out, "no pricing data")
}
