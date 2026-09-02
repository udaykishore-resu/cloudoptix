package compiler

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// EvaluateRegression runs every check in a RegressionSuite against a
// CompilationResult and produces the CI-facing report. It is a pure function
// of its two inputs — no I/O, no clock dependency beyond the report's
// timestamp — which is what makes "each RegressionCheckKind passing and
// failing" a fast, deterministic table test.
func EvaluateRegression(tenant core.TenantID, compilationID core.ID, suite simulate.RegressionSuite, result simulate.CompilationResult) simulate.RegressionReport {
	report := simulate.RegressionReport{
		ID:            core.NewID("rreg"),
		TenantID:      tenant,
		CompilationID: compilationID,
		SuiteName:     suite.Name,
		MonthlyDelta:  result.MonthlyDelta,
		AnnualDelta:   result.AnnualDelta,
	}
	for _, check := range suite.Checks {
		report.Results = append(report.Results, evaluateCheck(check, result))
	}
	report.Finalize()
	return report
}

// onViolation is the verdict a failed check reports: the check's own
// OnViolation when set, defaulting to FAIL — a check with no declared
// severity is safer to treat as blocking than to silently downgrade.
func onViolation(check simulate.RegressionCheck) simulate.Verdict {
	if check.OnViolation == simulate.VerdictWarning {
		return simulate.VerdictWarning
	}
	return simulate.VerdictFail
}

func resultMessage(check simulate.RegressionCheck, verdict simulate.Verdict, detail string) string {
	if verdict != simulate.VerdictPass && check.Message != "" {
		return check.Message
	}
	return detail
}

// checkApplies scopes a check to the environments it names. CompilationResult
// carries no Environment field of its own (see compiler.go's Compile for
// where the request's environment is stashed as an Assumption instead); this
// is the only reader of that stash.
func checkApplies(check simulate.RegressionCheck, result simulate.CompilationResult) bool {
	if len(check.Environments) == 0 {
		return true
	}
	env := requestEnvironment(result)
	for _, e := range check.Environments {
		if e == env {
			return true
		}
	}
	return false
}

func evaluateCheck(check simulate.RegressionCheck, result simulate.CompilationResult) simulate.CheckResult {
	if !checkApplies(check, result) {
		return simulate.CheckResult{
			Name: check.Name, Kind: check.Kind, Verdict: simulate.VerdictPass,
			Expected: "n/a", Actual: "n/a",
			Message: fmt.Sprintf("not applicable: scoped to %v, this change targets %s", check.Environments, requestEnvironment(result)),
		}
	}
	switch check.Kind {
	case simulate.CheckMaxMonthlyIncreasePct:
		return checkMaxIncreasePct(check, result)
	case simulate.CheckMaxMonthlyIncreaseAbs:
		return checkMaxIncreaseAbs(check, result)
	case simulate.CheckMaxCostPerTransaction:
		return checkMaxCostPerTransaction(check, result)
	case simulate.CheckForbiddenResource:
		return checkForbiddenResource(check, result)
	case simulate.CheckRequireTags:
		return checkRequireTags(check, result)
	case simulate.CheckMaxUnpricedRatio:
		return checkMaxUnpricedRatio(check, result)
	case simulate.CheckBudgetHeadroom:
		return checkBudgetHeadroom(check, result)
	default:
		return simulate.CheckResult{Name: check.Name, Kind: check.Kind, Verdict: simulate.VerdictWarning,
			Message: fmt.Sprintf("unknown check kind %q", check.Kind)}
	}
}

func checkMaxIncreasePct(check simulate.RegressionCheck, result simulate.CompilationResult) simulate.CheckResult {
	verdict := simulate.VerdictPass
	if result.DeltaPct > check.Threshold {
		verdict = onViolation(check)
	}
	return simulate.CheckResult{
		Name: check.Name, Kind: check.Kind, Verdict: verdict,
		Expected: fmt.Sprintf("<= %.1f%%", check.Threshold),
		Actual:   fmt.Sprintf("%.1f%%", result.DeltaPct),
		Message: resultMessage(check, verdict, fmt.Sprintf(
			"monthly cost would change %.1f%% (limit %.1f%%)", result.DeltaPct, check.Threshold)),
	}
}

func checkMaxIncreaseAbs(check simulate.RegressionCheck, result simulate.CompilationResult) simulate.CheckResult {
	verdict := simulate.VerdictPass
	if result.MonthlyDelta.GreaterThan(check.Amount) {
		verdict = onViolation(check)
	}
	return simulate.CheckResult{
		Name: check.Name, Kind: check.Kind, Verdict: verdict,
		Expected: "<= " + check.Amount.Format(),
		Actual:   result.MonthlyDelta.Format(),
		Message: resultMessage(check, verdict, fmt.Sprintf(
			"monthly cost would increase by %s (limit %s)", result.MonthlyDelta.Format(), check.Amount.Format())),
	}
}

// checkMaxCostPerTransaction reads the transaction's monthly volume from a
// well-known Assumption key ("transaction_volume_monthly:<name>", falling
// back to the unscoped "transaction_volume_monthly") that the caller
// populates via CompileRequest.Assumptions before compiling. In the absence
// of that figure the check reports WARNING with an explanatory message
// rather than silently passing or fabricating a volume — an unevaluable
// check is not the same claim as a satisfied one.
func checkMaxCostPerTransaction(check simulate.RegressionCheck, result simulate.CompilationResult) simulate.CheckResult {
	volume, ok := findAssumptionFloat(result, "transaction_volume_monthly:"+check.TransactionName)
	if !ok {
		volume, ok = findAssumptionFloat(result, "transaction_volume_monthly")
	}
	if !ok || volume <= 0 {
		return simulate.CheckResult{
			Name: check.Name, Kind: check.Kind, Verdict: simulate.VerdictWarning,
			Expected: "<= " + check.Amount.Format(), Actual: "unknown",
			Message: fmt.Sprintf(
				"cannot evaluate: no monthly transaction volume assumption found for %q (set transaction_volume_monthly:%s via CompileRequest.Assumptions)",
				check.TransactionName, check.TransactionName),
		}
	}
	costPerTx := result.ProjectedMonthly.Div(volume)
	verdict := simulate.VerdictPass
	if costPerTx.GreaterThan(check.Amount) {
		verdict = onViolation(check)
	}
	return simulate.CheckResult{
		Name: check.Name, Kind: check.Kind, Verdict: verdict,
		Expected: "<= " + check.Amount.Format(), Actual: costPerTx.Format(),
		Message: resultMessage(check, verdict, fmt.Sprintf(
			"projected cost per %s is %s (limit %s)", check.TransactionName, costPerTx.Format(), check.Amount.Format())),
	}
}

func checkForbiddenResource(check simulate.RegressionCheck, result simulate.CompilationResult) simulate.CheckResult {
	forbidden := make(map[string]bool, len(check.ResourceTypes))
	for _, t := range check.ResourceTypes {
		forbidden[t] = true
	}
	var offenders []string
	for _, c := range result.Changes {
		if c.Action == simulate.ChangeDelete {
			continue
		}
		if forbidden[c.ResourceType] {
			offenders = append(offenders, c.Address)
		}
	}
	verdict := simulate.VerdictPass
	if len(offenders) > 0 {
		verdict = onViolation(check)
	}
	return simulate.CheckResult{
		Name: check.Name, Kind: check.Kind, Verdict: verdict,
		Expected:  "none of: " + strings.Join(check.ResourceTypes, ", "),
		Actual:    fmt.Sprintf("%d found", len(offenders)),
		Message:   resultMessage(check, verdict, fmt.Sprintf("forbidden resource type(s) present: %s", strings.Join(offenders, ", "))),
		Offenders: offenders,
	}
}

// checkRequireTags reads each changed resource's tags back out of the
// tagWarningPrefix sentinel (see tags.go). A resource type this compiler
// never tags (a free resource with no PricedChange.Warnings tag entry) is not
// an offender — it is outside the check's concern entirely.
func checkRequireTags(check simulate.RegressionCheck, result simulate.CompilationResult) simulate.CheckResult {
	typeFilter := make(map[string]bool, len(check.ResourceTypes))
	for _, t := range check.ResourceTypes {
		typeFilter[t] = true
	}
	var offenders []string
	for _, c := range result.Changes {
		if c.Action == simulate.ChangeDelete {
			continue
		}
		if len(typeFilter) > 0 && !typeFilter[c.ResourceType] {
			continue
		}
		tags, found := parseTagWarning(c.Warnings)
		if !found {
			continue
		}
		for _, req := range check.RequiredTags {
			if !tags[req] {
				offenders = append(offenders, fmt.Sprintf("%s (missing %s)", c.Address, req))
				break
			}
		}
	}
	verdict := simulate.VerdictPass
	if len(offenders) > 0 {
		verdict = onViolation(check)
	}
	return simulate.CheckResult{
		Name: check.Name, Kind: check.Kind, Verdict: verdict,
		Expected:  "tags present: " + strings.Join(check.RequiredTags, ", "),
		Actual:    fmt.Sprintf("%d resource(s) missing a required tag", len(offenders)),
		Message:   resultMessage(check, verdict, fmt.Sprintf("missing required tags: %s", strings.Join(offenders, "; "))),
		Offenders: offenders,
	}
}

func checkMaxUnpricedRatio(check simulate.RegressionCheck, result simulate.CompilationResult) simulate.CheckResult {
	ratio := 1 - result.Coverage
	verdict := simulate.VerdictPass
	if ratio > check.Threshold {
		verdict = onViolation(check)
	}
	return simulate.CheckResult{
		Name: check.Name, Kind: check.Kind, Verdict: verdict,
		Expected: fmt.Sprintf("<= %.0f%% unpriced", check.Threshold*100),
		Actual:   fmt.Sprintf("%.0f%% unpriced", ratio*100),
		Message: resultMessage(check, verdict, fmt.Sprintf(
			"%.0f%% of changed resources could not be priced (limit %.0f%%); the delta is not trustworthy above this ratio", ratio*100, check.Threshold*100)),
	}
}

func checkBudgetHeadroom(check simulate.RegressionCheck, result simulate.CompilationResult) simulate.CheckResult {
	verdict := simulate.VerdictPass
	if result.ProjectedMonthly.GreaterThan(check.Amount) {
		verdict = onViolation(check)
	}
	return simulate.CheckResult{
		Name: check.Name, Kind: check.Kind, Verdict: verdict,
		Expected: "projected monthly total <= " + check.Amount.Format(),
		Actual:   result.ProjectedMonthly.Format(),
		Message: resultMessage(check, verdict, fmt.Sprintf(
			"projected monthly total %s exceeds the %s economic error budget", result.ProjectedMonthly.Format(), check.Amount.Format())),
	}
}

func findAssumptionFloat(result simulate.CompilationResult, key string) (float64, bool) {
	for _, a := range result.Assumptions {
		if a.Key == key {
			f, err := strconv.ParseFloat(a.Value, 64)
			if err != nil {
				return 0, false
			}
			return f, true
		}
	}
	return 0, false
}
