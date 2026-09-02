package compiler

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

func baseResult() simulate.CompilationResult {
	r := simulate.CompilationResult{
		Changes: []simulate.PricedChange{
			{Address: "aws_instance.api", ResourceType: "aws_instance", Action: simulate.ChangeCreate,
				BeforeMonthly: core.ZeroUSD(), AfterMonthly: core.USDollars(100), MonthlyDelta: core.USDollars(100),
				Warnings: []string{"tags:owner,environment"}},
			{Address: "aws_nat_gateway.eg", ResourceType: "aws_nat_gateway", Action: simulate.ChangeCreate,
				BeforeMonthly: core.ZeroUSD(), AfterMonthly: core.USDollars(50), MonthlyDelta: core.USDollars(50),
				Warnings: []string{"tags:"}},
		},
		Assumptions: []simulate.Assumption{{Key: "environment", Value: "production"}},
	}
	r.Summarize()
	return r
}

func TestCheck_MaxMonthlyIncreasePct(t *testing.T) {
	result := baseResult() // baseline 0, projected 150 -> DeltaPct is 0 because baseline is 0 (Ratio returns 0)
	result.BaselineMonthly = core.USDollars(500)
	result.ProjectedMonthly = core.USDollars(650)
	result.MonthlyDelta = core.USDollars(150)
	result.DeltaPct = 30

	pass := evaluateCheck(simulate.RegressionCheck{Name: "pct", Kind: simulate.CheckMaxMonthlyIncreasePct, Threshold: 50, OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictPass, pass.Verdict)

	fail := evaluateCheck(simulate.RegressionCheck{Name: "pct", Kind: simulate.CheckMaxMonthlyIncreasePct, Threshold: 10, OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictFail, fail.Verdict)

	warn := evaluateCheck(simulate.RegressionCheck{Name: "pct", Kind: simulate.CheckMaxMonthlyIncreasePct, Threshold: 10, OnViolation: simulate.VerdictWarning}, result)
	assert.Equal(t, simulate.VerdictWarning, warn.Verdict)
}

func TestCheck_MaxMonthlyIncreaseAbs(t *testing.T) {
	result := baseResult()
	result.MonthlyDelta = core.USDollars(150)

	pass := evaluateCheck(simulate.RegressionCheck{Name: "abs", Kind: simulate.CheckMaxMonthlyIncreaseAbs, Amount: core.USDollars(200), OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictPass, pass.Verdict)

	fail := evaluateCheck(simulate.RegressionCheck{Name: "abs", Kind: simulate.CheckMaxMonthlyIncreaseAbs, Amount: core.USDollars(100), OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictFail, fail.Verdict)
}

func TestCheck_MaxCostPerTransaction(t *testing.T) {
	result := baseResult()
	result.ProjectedMonthly = core.USDollars(1000)

	// No volume assumption present at all: an unevaluable check reports
	// WARNING with an explanation, never a silent pass.
	unknown := evaluateCheck(simulate.RegressionCheck{Name: "cpt", Kind: simulate.CheckMaxCostPerTransaction, TransactionName: "checkout", Amount: core.USDollars(1), OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictWarning, unknown.Verdict)
	assert.Contains(t, unknown.Message, "cannot evaluate")

	result.Assumptions = append(result.Assumptions, simulate.Assumption{Key: "transaction_volume_monthly:checkout", Value: "500000"})
	// $1000 / 500,000 = $0.002/tx
	pass := evaluateCheck(simulate.RegressionCheck{Name: "cpt", Kind: simulate.CheckMaxCostPerTransaction, TransactionName: "checkout", Amount: core.USDollars(0.01), OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictPass, pass.Verdict)

	fail := evaluateCheck(simulate.RegressionCheck{Name: "cpt", Kind: simulate.CheckMaxCostPerTransaction, TransactionName: "checkout", Amount: core.USDollars(0.001), OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictFail, fail.Verdict)
}

func TestCheck_ForbiddenResource(t *testing.T) {
	result := baseResult()
	pass := evaluateCheck(simulate.RegressionCheck{Name: "forbid", Kind: simulate.CheckForbiddenResource, ResourceTypes: []string{"aws_msk_cluster"}, OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictPass, pass.Verdict)

	fail := evaluateCheck(simulate.RegressionCheck{Name: "forbid", Kind: simulate.CheckForbiddenResource, ResourceTypes: []string{"aws_nat_gateway"}, OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictFail, fail.Verdict)
	assert.Contains(t, fail.Offenders, "aws_nat_gateway.eg")
}

func TestCheck_RequireTags(t *testing.T) {
	result := baseResult()
	// aws_instance.api carries owner+environment; aws_nat_gateway.eg carries none.
	pass := evaluateCheck(simulate.RegressionCheck{Name: "tags", Kind: simulate.CheckRequireTags, ResourceTypes: []string{"aws_instance"}, RequiredTags: []string{"owner"}, OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictPass, pass.Verdict)

	fail := evaluateCheck(simulate.RegressionCheck{Name: "tags", Kind: simulate.CheckRequireTags, RequiredTags: []string{"owner"}, OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictFail, fail.Verdict)
	assert.Contains(t, fail.Offenders[0], "aws_nat_gateway.eg")
}

func TestCheck_MaxUnpricedRatio(t *testing.T) {
	result := baseResult()
	result.Changes = append(result.Changes, simulate.PricedChange{Address: "aws_msk_cluster.x", Unpriced: true, UnpricedReason: "no pricing"})
	result.Summarize()
	// 1 unpriced out of 3 -> 33% unpriced.
	pass := evaluateCheck(simulate.RegressionCheck{Name: "unpriced", Kind: simulate.CheckMaxUnpricedRatio, Threshold: 0.5, OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictPass, pass.Verdict)

	fail := evaluateCheck(simulate.RegressionCheck{Name: "unpriced", Kind: simulate.CheckMaxUnpricedRatio, Threshold: 0.1, OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictFail, fail.Verdict)
}

func TestCheck_BudgetHeadroom(t *testing.T) {
	result := baseResult()
	result.ProjectedMonthly = core.USDollars(900)
	pass := evaluateCheck(simulate.RegressionCheck{Name: "budget", Kind: simulate.CheckBudgetHeadroom, Amount: core.USDollars(1000), OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictPass, pass.Verdict)

	fail := evaluateCheck(simulate.RegressionCheck{Name: "budget", Kind: simulate.CheckBudgetHeadroom, Amount: core.USDollars(500), OnViolation: simulate.VerdictFail}, result)
	assert.Equal(t, simulate.VerdictFail, fail.Verdict)
}

func TestCheck_EnvironmentScoping(t *testing.T) {
	result := baseResult() // environment assumption = "production"
	result.MonthlyDelta = core.USDollars(999999)

	notApplicable := evaluateCheck(simulate.RegressionCheck{
		Name: "staging-only", Kind: simulate.CheckMaxMonthlyIncreaseAbs, Amount: core.USDollars(1),
		Environments: []core.Environment{core.EnvStaging}, OnViolation: simulate.VerdictFail,
	}, result)
	assert.Equal(t, simulate.VerdictPass, notApplicable.Verdict)
	assert.Contains(t, notApplicable.Message, "not applicable")

	applicable := evaluateCheck(simulate.RegressionCheck{
		Name: "prod-only", Kind: simulate.CheckMaxMonthlyIncreaseAbs, Amount: core.USDollars(1),
		Environments: []core.Environment{core.EnvProduction}, OnViolation: simulate.VerdictFail,
	}, result)
	assert.Equal(t, simulate.VerdictFail, applicable.Verdict)
}

func TestEvaluateRegression_FinalizesOverallVerdict(t *testing.T) {
	result := baseResult()
	result.MonthlyDelta = core.USDollars(150)
	suite := simulate.RegressionSuite{
		Name: "default",
		Checks: []simulate.RegressionCheck{
			{Name: "abs-warn", Kind: simulate.CheckMaxMonthlyIncreaseAbs, Amount: core.USDollars(100), OnViolation: simulate.VerdictWarning},
			{Name: "forbid-fail", Kind: simulate.CheckForbiddenResource, ResourceTypes: []string{"aws_nat_gateway"}, OnViolation: simulate.VerdictFail},
		},
	}
	report := EvaluateRegression("t1", "cmp_1", suite, result)
	assert.Equal(t, simulate.VerdictFail, report.Verdict, "worst check wins")
	assert.Equal(t, "Architecture review required.", report.RequiredAction)
	assert.Len(t, report.Results, 2)
}
