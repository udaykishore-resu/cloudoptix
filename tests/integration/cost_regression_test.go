package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/application/compiler"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const fixtureDir = "../testdata/costregression"

// TestCostRegressionSuite compiles real Terraform plan documents and
// evaluates them against a real regression suite, asserting each of the
// three verdicts the CI gate distinguishes.
//
// All three matter and for different reasons. PASS is what lets a pull
// request merge, so a suite that could never pass would be turned off within
// a week. FAIL is the gate itself. WARNING is the one most easily broken by
// a well-meaning change, because "it did not fail" and "it passed" look
// identical from the outside — a coverage warning silently downgraded to a
// pass would let an unpriced change through with a confident-looking delta.
//
// Traceability: REQ-COMP-006, SPEC-COMP-003.
func TestCostRegressionSuite(t *testing.T) {
	suite := loadSuite(t, filepath.Join(fixtureDir, "suite.yaml"))

	cases := []struct {
		file    string
		verdict simulate.Verdict
		// why names the check expected to drive the verdict, so a fixture
		// that reaches the right verdict for the wrong reason still fails.
		why string
	}{
		{"plan-pass.json", simulate.VerdictPass, ""},
		{"plan-warning.json", simulate.VerdictWarning, "pricing-coverage-floor"},
		{"plan-fail.json", simulate.VerdictFail, "no-new-nat-gateways"},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			result := compileFixture(t, tc.file)
			report := compiler.EvaluateRegression(core.TenantID("cli"), result.ID, suite, result)

			assert.Equal(t, tc.verdict, report.Verdict,
				"summary: %s; results: %s", report.Summary, describeResults(report))
			require.Len(t, report.Results, len(suite.Checks),
				"every check must produce a result, including the ones that passed")

			if tc.why != "" {
				outcome, ok := findResult(report, tc.why)
				require.True(t, ok, "expected a result for check %q", tc.why)
				assert.Equal(t, tc.verdict, outcome.Verdict,
					"check %q did not drive the verdict: %s", tc.why, outcome.Message)
			}
		})
	}

	t.Run("a passing plan prices every change", func(t *testing.T) {
		result := compileFixture(t, "plan-pass.json")
		assert.Equal(t, 0, result.UnpricedCount)
		assert.InDelta(t, 1.0, result.Coverage, 1e-9)
		assert.True(t, result.MonthlyDelta.IsNegative(),
			"the fixture downsizes an instance and migrates gp2 to gp3; the delta must be a saving, got %s",
			result.MonthlyDelta.Format())
		assert.NotEmpty(t, result.Changes)
	})

	t.Run("an unpriceable resource is counted, not ignored", func(t *testing.T) {
		result := compileFixture(t, "plan-warning.json")
		assert.Greater(t, result.UnpricedCount, 0,
			"the fixture creates two resource types the price book does not cover")
		assert.Less(t, result.Coverage, 1.0)

		// The unpriced resources must appear in the change list with a
		// reason. Dropping them would make the delta look complete when it
		// is not, which is the specific dishonesty the coverage figure
		// exists to prevent.
		var unpriced int
		for _, c := range result.Changes {
			if c.Unpriced {
				unpriced++
				assert.NotEmpty(t, c.UnpricedReason, "%s is unpriced with no reason given", c.Address)
			}
		}
		assert.Equal(t, result.UnpricedCount, unpriced)
	})

	t.Run("a failing plan reports what to do about it", func(t *testing.T) {
		result := compileFixture(t, "plan-fail.json")
		report := compiler.EvaluateRegression(core.TenantID("cli"), result.ID, suite, result)

		require.Equal(t, simulate.VerdictFail, report.Verdict)
		assert.NotEmpty(t, report.RequiredAction,
			"a FAIL with no required action tells a reviewer nothing")
		assert.True(t, report.MonthlyDelta.GreaterThan(core.ZeroUSD()))

		offenders, ok := findResult(report, "new-resources-must-be-tagged")
		require.True(t, ok)
		assert.NotEmpty(t, offenders.Offenders,
			"the untagged-resource check must name the resource, not just the count")

		// A cost risk is not a check result: it is the compiler noticing a
		// structural hazard on its own. The three-NAT-gateway shape is
		// exactly what it exists to catch.
		assert.NotEmpty(t, result.Risks, "adding NAT gateways must raise a cost risk")
		assert.NotEmpty(t, result.Opportunities,
			"a NAT gateway with no companion S3 gateway endpoint is a free saving the compiler should offer")
	})

	t.Run("the PR comment renders the verdict a reviewer acts on", func(t *testing.T) {
		result := compileFixture(t, "plan-fail.json")
		report := compiler.EvaluateRegression(core.TenantID("cli"), result.ID, suite, result)
		comment := compiler.RenderPRComment(result, &report)

		assert.Contains(t, comment, "FAIL")
		assert.Contains(t, comment, "no-new-nat-gateways")
		assert.Contains(t, comment, result.MonthlyDelta.Format())
		assert.Contains(t, comment, "module.network.aws_nat_gateway.az_a",
			"the comment must name the offending resource, not just the totals")
	})
}

func compileFixture(t *testing.T, name string) simulate.CompilationResult {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(fixtureDir, name))
	require.NoError(t, err)

	c := compiler.New(pricing.New())
	result, err := c.Compile(core.TenantID("cli"), ports.CompileRequest{
		Source: simulate.SourceTerraformPlan, Label: name, Content: content,
		Region: core.Region("us-east-1"), Environment: core.EnvProduction,
		RequestedBy: "integration-test",
	})
	require.NoError(t, err)
	return result
}

func loadSuite(t *testing.T, path string) simulate.RegressionSuite {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var suite simulate.RegressionSuite
	require.NoError(t, yaml.Unmarshal(raw, &suite))
	require.NotEmpty(t, suite.Checks, "a suite with no checks would pass everything")
	suite.TenantID = core.TenantID("cli")
	return suite
}

func findResult(report simulate.RegressionReport, name string) (simulate.CheckResult, bool) {
	for _, r := range report.Results {
		if r.Name == name {
			return r, true
		}
	}
	return simulate.CheckResult{}, false
}

func describeResults(report simulate.RegressionReport) string {
	out := ""
	for _, r := range report.Results {
		out += r.Name + "=" + string(r.Verdict) + " "
	}
	return out
}
