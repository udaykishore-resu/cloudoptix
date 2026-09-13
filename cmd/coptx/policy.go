package main

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/app"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// policyValidateOutput is the --json shape.
type policyValidateOutput struct {
	File          string        `json:"file"`
	Name          string        `json:"name"`
	Version       int           `json:"version"`
	DefaultEffect govern.Effect `json:"default_effect"`
	RuleCount     int           `json:"rule_count"`
	Valid         bool          `json:"valid"`
	Issues        []issueJSON   `json:"issues"`
}

func loadPolicyFile(path string) (govern.Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return govern.Policy{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var p govern.Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return govern.Policy{}, fmt.Errorf("%s is not a valid policy document: %w", path, err)
	}
	return p, nil
}

func runPolicyValidate(args []string) (int, error) {
	fs := newFlagSet("policy validate")
	asJSON := fs.Bool("json", false, "emit JSON")
	handled, err := parseFlags(fs, args)
	if handled {
		return exitFor(err), err
	}
	if fs.NArg() == 0 {
		return 2, fmt.Errorf("usage: coptx policy validate <file>")
	}
	path := fs.Arg(0)

	policy, err := loadPolicyFile(path)
	if err != nil {
		return 1, err
	}
	validation := policy.Validate()

	if *asJSON {
		out := policyValidateOutput{
			File: path, Name: policy.Name, Version: policy.Version,
			DefaultEffect: policy.DefaultEffect, RuleCount: len(policy.Rules),
			Valid: !validation.HasBlocking(),
		}
		for _, iss := range validation.Issues {
			out.Issues = append(out.Issues, issueJSON{
				Path: iss.Path, Code: iss.Code, Severity: string(iss.Severity),
				Message: iss.Message, Hint: iss.Hint,
			})
		}
		if err := writeJSON(os.Stdout, out); err != nil {
			return 1, err
		}
		return exitCodeForValidation(validation.HasBlocking()), nil
	}

	fmt.Printf("Policy:         %s (v%d) from %s\n", policy.Name, policy.Version, path)
	fmt.Printf("Default effect: %s\n", policy.DefaultEffect)
	fmt.Printf("Rules:          %d\n\n", len(policy.Rules))
	blocking := printIssues(os.Stdout, "Validation", validation)
	return exitCodeForValidation(blocking), nil
}

func runPolicySimulate(args []string) (int, error) {
	fs := newFlagSet("policy simulate")
	asJSON := fs.Bool("json", false, "emit JSON")
	handled, err := parseFlags(fs, args)
	if handled {
		return exitFor(err), err
	}
	if fs.NArg() == 0 {
		return 2, fmt.Errorf("usage: coptx policy simulate <file>")
	}
	path := fs.Arg(0)

	policy, err := loadPolicyFile(path)
	if err != nil {
		return 1, err
	}
	if validation := policy.Validate(); validation.HasBlocking() {
		printIssues(os.Stderr, "Validation", validation)
		return 1, fmt.Errorf("%s has blocking validation issues; there is no point simulating a policy that cannot be saved", path)
	}

	// Simulating a policy means answering "what would this do to my
	// recommendations", which needs recommendations. Outside a deployment
	// the only ones available are the demo tenant's, so the CLI seeds it —
	// which is also what makes this command a genuinely useful preview of a
	// policy edit rather than a syntax check with extra steps.
	ctx := context.Background()
	application, seeded, err := bootDemo(ctx)
	if err != nil {
		return 1, err
	}
	defer func() { _ = application.Close() }()

	tctx := core.WithPrincipal(ctx, core.Principal{
		Subject: "coptx", TenantID: seeded.TenantID,
		Roles: []core.Role{core.RoleTenantAdmin},
	})
	policy.TenantID = seeded.TenantID
	sim, err := application.Services.Governance.Simulate(tctx, seeded.TenantID, policy)
	if err != nil {
		return 1, fmt.Errorf("simulating policy %q: %w", policy.Name, err)
	}

	if *asJSON {
		return 0, writeJSON(os.Stdout, sim)
	}
	printPolicySimulation(policy, sim)
	return 0, nil
}

func printPolicySimulation(policy govern.Policy, sim ports.PolicySimulation) {
	fmt.Printf("Policy simulation: %s (v%d)\n", policy.Name, policy.Version)
	fmt.Printf("Evaluated against %d open recommendation(s) of the demo tenant.\n\n", sim.Evaluated)

	tw := newTable(os.Stdout)
	fmt.Fprintln(tw, "  EFFECT\tCOUNT")
	fmt.Fprintf(tw, "  auto_execute\t%d\n", sim.AutoExecute)
	fmt.Fprintf(tw, "  require_approval\t%d\n", sim.RequireApproval)
	fmt.Fprintf(tw, "  prohibit\t%d\n", sim.Prohibited)
	fmt.Fprintf(tw, "  advisory_only\t%d\n", sim.Advisory)
	_ = tw.Flush()

	fmt.Printf("\n  Auto-executable saving: %s/month\n", sim.AutoExecutableSaving.Format())

	if len(sim.Changes) > 0 {
		fmt.Printf("\nWould change the outcome for %d recommendation(s):\n", len(sim.Changes))
		ct := newTable(os.Stdout)
		fmt.Fprintln(ct, "  FROM\tTO\tSAVING\tRECOMMENDATION")
		shown := sim.Changes
		if len(shown) > 20 {
			shown = shown[:20]
		}
		for _, c := range shown {
			fmt.Fprintf(ct, "  %s\t%s\t%s\t%s\n", c.From, c.To, c.MonthlySaving.Format(), c.Title)
		}
		_ = ct.Flush()
		if len(sim.Changes) > len(shown) {
			fmt.Printf("  ... and %d more (use --json for the full list)\n", len(sim.Changes)-len(shown))
		}
	}

	for _, w := range sim.Warnings {
		fmt.Printf("\n  warning: %s\n", w)
	}
	fmt.Println()
}

// bootDemo builds a zero-infrastructure application and seeds the demo
// tenant. Every CLI command that needs tenant data goes through it, so
// "coptx works with nothing installed" is one function's responsibility
// rather than a property each command has to remember to preserve.
func bootDemo(ctx context.Context) (*app.App, *app.SeedResult, error) {
	application, err := buildDemoApp(ctx)
	if err != nil {
		return nil, nil, err
	}
	seeded, err := app.Seed(ctx, application)
	if err != nil {
		_ = application.Close()
		return nil, nil, fmt.Errorf("seeding the demo tenant: %w", err)
	}
	return application, seeded, nil
}
