package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/application/compiler"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// ciTenant is the tenant id compilations run under outside a deployment.
// The compiler is a pure function of the change set and the price book — it
// reads no tenant data — but simulate.CompilationResult carries a tenant
// field, so a fixed, obviously-synthetic value is better than an empty one
// that could be mistaken for a real tenant's compilation if the result is
// ever imported.
const ciTenant core.TenantID = "cli"

// sourceKindFor maps the --source flag to the compiler's SourceKind,
// rejecting anything else by name rather than defaulting: silently
// compiling a CloudFormation template as a Terraform plan would produce zero
// changes and a confident "$0.00 delta", which is worse than an error.
func sourceKindFor(name string) (simulate.SourceKind, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "terraform-plan", "terraform_plan", "tfplan":
		return simulate.SourceTerraformPlan, nil
	case "terraform-hcl", "terraform_hcl", "hcl":
		return simulate.SourceTerraformHCL, nil
	case "cloudformation", "cfn":
		return simulate.SourceCloudFormation, nil
	case "kubernetes", "k8s":
		return simulate.SourceKubernetes, nil
	default:
		return "", fmt.Errorf("unknown --source %q (want terraform-plan, terraform-hcl, cloudformation or kubernetes)", name)
	}
}

func compileFile(source, file, region, environment, label string) (simulate.CompilationResult, error) {
	kind, err := sourceKindFor(source)
	if err != nil {
		return simulate.CompilationResult{}, err
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return simulate.CompilationResult{}, fmt.Errorf("reading %s: %w", file, err)
	}
	if label == "" {
		label = file
	}
	c := compiler.New(pricing.New())
	return c.Compile(ciTenant, ports.CompileRequest{
		Source: kind, Label: label, Content: content,
		Region: core.Region(region), Environment: core.Environment(environment),
		RequestedBy: "coptx",
	})
}

func runCostCompile(args []string) (int, error) {
	fs := newFlagSet("cost compile")
	source := fs.String("source", "terraform-plan", "change-set format: terraform-plan|terraform-hcl|cloudformation|kubernetes")
	file := fs.String("file", "", "path to the change set (required)")
	region := fs.String("region", "us-east-1", "region to price against")
	environment := fs.String("environment", "production", "environment the change targets")
	label := fs.String("label", "", "human label for the compilation (defaults to the file name)")
	asJSON := fs.Bool("json", false, "emit JSON")
	handled, err := parseFlags(fs, args)
	if handled {
		return exitFor(err), err
	}
	if *file == "" {
		return 2, fmt.Errorf("--file is required")
	}

	result, err := compileFile(*source, *file, *region, *environment, *label)
	if err != nil {
		return 1, err
	}

	if *asJSON {
		return 0, writeJSON(os.Stdout, result)
	}
	printCompilation(result)
	return 0, nil
}

func printCompilation(r simulate.CompilationResult) {
	fmt.Printf("Cost compilation: %s (%s)\n", r.Label, r.Source)
	fmt.Printf("Priced against the %s price book.\n\n", r.PricingDate.Format("2006-01-02"))

	if len(r.Changes) == 0 {
		fmt.Println("No resource changes in this change set.")
		return
	}

	tw := newTable(os.Stdout)
	fmt.Fprintln(tw, "  ACTION\tADDRESS\tBEFORE\tAFTER\tDELTA")
	for _, c := range r.Changes {
		after := c.AfterMonthly.Format()
		if c.Unpriced {
			after = "unpriced"
		} else if c.UsageDependent {
			after += " (est.)"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			c.Action, c.Address, c.BeforeMonthly.Format(), after, signedMoney(c.MonthlyDelta))
	}
	_ = tw.Flush()

	fmt.Printf("\n  %-22s %s -> %s\n", "Monthly", r.BaselineMonthly.Format(), r.ProjectedMonthly.Format())
	// DeltaPct is already expressed in percent (see
	// simulate.CompilationResult.Summarize), so it is printed as-is.
	fmt.Printf("  %-22s %s (%+.1f%%)\n", "Monthly delta", signedMoney(r.MonthlyDelta), r.DeltaPct)
	fmt.Printf("  %-22s %s\n", "Annual delta", signedMoney(r.AnnualDelta))
	fmt.Printf("  %-22s +%d created, ~%d updated, -%d deleted, %d unpriced\n",
		"Changes", r.CreatedCount, r.UpdatedCount, r.DeletedCount, r.UnpricedCount)
	fmt.Printf("  %-22s %.0f%% (confidence %s)\n", "Pricing coverage", r.Coverage*100, r.Confidence)

	if len(r.Assumptions) > 0 {
		fmt.Printf("\nAssumptions (%d) — every usage-dependent figure above rests on these:\n", len(r.Assumptions))
		for _, a := range r.Assumptions {
			fmt.Printf("  • %s = %s %s (%s)\n", a.Label, a.Value, a.Unit, a.Provenance)
		}
	}

	if len(r.Risks) > 0 {
		fmt.Printf("\nCost risks (%d):\n", len(r.Risks))
		for _, risk := range r.Risks {
			fmt.Printf("  [%s] %s\n", risk.Severity, risk.Summary)
			if risk.Address != "" {
				fmt.Printf("        at %s\n", risk.Address)
			}
			if !risk.MonthlyImpact.IsZero() {
				fmt.Printf("        impact %s/month\n", risk.MonthlyImpact.Format())
			}
			if risk.Remediation != "" {
				fmt.Printf("        fix: %s\n", risk.Remediation)
			}
		}
	}

	if len(r.Opportunities) > 0 {
		fmt.Printf("\nOpportunities (%d) — cheaper alternatives, free to take before merge:\n", len(r.Opportunities))
		for _, o := range r.Opportunities {
			fmt.Printf("  • %s\n        %s saves %s/month\n", o.Summary, o.Address, o.MonthlySaving.Format())
		}
	}
	fmt.Println()
}

// costTestOutput is the --json shape: the report plus the compilation it was
// evaluated against, because a FAIL verdict is not actionable without the
// numbers behind it.
type costTestOutput struct {
	Verdict     simulate.Verdict           `json:"verdict"`
	Report      simulate.RegressionReport  `json:"report"`
	Compilation simulate.CompilationResult `json:"compilation"`
}

func runCostTest(args []string) (int, error) {
	fs := newFlagSet("cost test")
	source := fs.String("source", "terraform-plan", "change-set format: terraform-plan|terraform-hcl|cloudformation|kubernetes")
	file := fs.String("file", "", "path to the change set (required)")
	suitePath := fs.String("suite", "", "path to the regression suite YAML (required)")
	region := fs.String("region", "us-east-1", "region to price against")
	environment := fs.String("environment", "production", "environment the change targets")
	label := fs.String("label", "", "human label for the compilation (defaults to the file name)")
	format := fs.String("format", "text", "output format: text|markdown")
	asJSON := fs.Bool("json", false, "emit JSON (overrides --format)")
	out := fs.String("out", "", "also write the rendered output to this file (for a PR-comment step)")
	handled, err := parseFlags(fs, args)
	if handled {
		return exitFor(err), err
	}
	if *file == "" {
		return 2, fmt.Errorf("--file is required")
	}
	if *suitePath == "" {
		return 2, fmt.Errorf("--suite is required")
	}

	suiteYAML, err := os.ReadFile(*suitePath)
	if err != nil {
		return 1, fmt.Errorf("reading %s: %w", *suitePath, err)
	}
	var suite simulate.RegressionSuite
	if err := yaml.Unmarshal(suiteYAML, &suite); err != nil {
		return 1, fmt.Errorf("%s is not a valid regression suite: %w", *suitePath, err)
	}
	if len(suite.Checks) == 0 {
		return 1, fmt.Errorf("%s declares no checks; a suite with no checks would pass everything", *suitePath)
	}
	suite.TenantID = ciTenant

	result, err := compileFile(*source, *file, *region, *environment, *label)
	if err != nil {
		return 1, err
	}
	report := compiler.EvaluateRegression(ciTenant, result.ID, suite, result)

	var rendered string
	switch {
	case *asJSON:
		if err := writeJSON(os.Stdout, costTestOutput{
			Verdict: report.Verdict, Report: report, Compilation: result,
		}); err != nil {
			return 1, err
		}
	case strings.EqualFold(*format, "markdown"):
		rendered = compiler.RenderPRComment(result, &report)
		fmt.Print(rendered)
	default:
		printCompilation(result)
		rendered = renderVerdict(report)
		fmt.Print(rendered)
	}

	if *out != "" {
		if rendered == "" {
			rendered = compiler.RenderPRComment(result, &report)
		}
		if err := os.WriteFile(*out, []byte(rendered), 0o644); err != nil {
			return 1, fmt.Errorf("writing %s: %w", *out, err)
		}
	}

	// FAIL exits non-zero so the CI step actually gates the pull request. A
	// WARNING does not: the whole point of a distinct warning verdict is that
	// it is visible without being a wall.
	if report.Verdict == simulate.VerdictFail {
		return 1, nil
	}
	return 0, nil
}

func renderVerdict(report simulate.RegressionReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cost regression suite: %s\n", report.SuiteName)
	fmt.Fprintf(&b, "Verdict: %s — %s\n", report.Verdict, report.Summary)
	if report.RequiredAction != "" {
		fmt.Fprintf(&b, "Required action: %s\n", report.RequiredAction)
	}
	fmt.Fprintln(&b)
	tw := newTable(&b)
	fmt.Fprintln(tw, "  VERDICT\tCHECK\tEXPECTED\tACTUAL")
	for _, res := range report.Results {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", res.Verdict, res.Name, res.Expected, res.Actual)
	}
	_ = tw.Flush()
	fmt.Fprintln(&b)
	return b.String()
}
