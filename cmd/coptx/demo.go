package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/app"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/infrastructure/config"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// buildDemoApp constructs the zero-infrastructure application: memory
// storage, the simulated estate, the deterministic model provider, an
// in-process bus. It builds the configuration in code rather than reading
// the environment, because `coptx demo` must behave identically on a laptop
// with CLOUDOPTIX_* variables left over from something else.
func buildDemoApp(ctx context.Context) (*app.App, error) {
	cfg := config.Defaults()
	cfg.Environment = "development"
	cfg.Storage = config.StorageMemory
	cfg.Cache = config.CacheMemory
	cfg.Events.Kind = config.EventsInProcess
	cfg.AWS.Mode = config.AWSModeSimulated
	cfg.LLM.Provider = config.LLMProviderScripted
	cfg.Features.AutonomousExecution = true
	cfg.Telemetry.MetricsEnabled = false
	cfg.Auth.DevStaticTokenEnabled = true
	cfg.Auth.DevStaticToken = config.NewLiteralSecret("coptx-local")
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// The CLI's own output is the product here; the platform's structured
	// logs would drown it. Warnings and errors still reach stderr, so a
	// failure is never silent.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return app.Build(ctx, cfg, logger)
}

func demoPrincipal(tenant core.TenantID) core.Principal {
	return core.Principal{
		Subject: "coptx", TenantID: tenant, Email: "coptx@localhost",
		Roles: []core.Role{core.RoleTenantAdmin}, IssuedAt: time.Now().UTC(),
	}
}

func runDemoSeed(args []string) (int, error) {
	fs := newFlagSet("demo seed")
	asJSON := fs.Bool("json", false, "emit JSON")
	handled, err := parseFlags(fs, args)
	if handled {
		return exitFor(err), err
	}

	ctx := context.Background()
	application, seeded, err := bootDemo(ctx)
	if err != nil {
		return 1, err
	}
	defer func() { _ = application.Close() }()

	if *asJSON {
		return 0, writeJSON(os.Stdout, seeded)
	}
	seeded.PrintSummary(os.Stdout)
	return 0, nil
}

// demoRunOutput is the --json shape of `demo run`: one entry per stage, so a
// machine consumer can assert the flow completed without parsing prose.
type demoRunOutput struct {
	Tenant core.TenantID   `json:"tenant"`
	Seed   *app.SeedResult `json:"seed"`
	Stages []demoStage     `json:"stages"`
}

type demoStage struct {
	Name    string         `json:"name"`
	Detail  string         `json:"detail"`
	Data    map[string]any `json:"data,omitempty"`
	Skipped bool           `json:"skipped,omitempty"`
}

func runDemoRun(args []string) (int, error) {
	fs := newFlagSet("demo run")
	asJSON := fs.Bool("json", false, "emit JSON")
	handled, err := parseFlags(fs, args)
	if handled {
		return exitFor(err), err
	}

	ctx := context.Background()
	application, seeded, err := bootDemo(ctx)
	if err != nil {
		return 1, err
	}
	defer func() { _ = application.Close() }()

	out := demoRunOutput{Tenant: seeded.TenantID, Seed: seeded}
	human := io.Writer(os.Stdout)
	if *asJSON {
		human = io.Discard
	}

	stage := func(name, format string, args ...any) {
		detail := fmt.Sprintf(format, args...)
		out.Stages = append(out.Stages, demoStage{Name: name, Detail: detail})
		fmt.Fprintf(human, "  %-22s %s\n", name, detail)
	}

	if !*asJSON {
		seeded.PrintSummary(os.Stdout)
		fmt.Println("  Driving the optimization flow")
		fmt.Println("  " + dashes(68))
	}

	stage("seeded", "%d resources, %s/month, %d recommendations worth %s/month",
		seeded.ResourcesDiscovered, seeded.MonthlySpend.Format(),
		seeded.Recommendations, seeded.MonthlySaving.Format())

	tctx := core.WithPrincipal(ctx, demoPrincipal(seeded.TenantID))
	svcs := application.Services

	// The autonomous pass runs first, and it runs against the same estate and
	// the same policy the human-approval flow below uses. That ordering is
	// the demonstration: one pass, one policy, two outcomes — the sandbox's
	// unambiguous waste is gone before anyone is asked anything, and the
	// production change immediately after it still stops and waits for a
	// person. A demo that only ever showed the approval path could not tell
	// those two apart, and a platform that claims autonomy has to show it
	// declining to be autonomous just as clearly as it shows it acting.
	autonomous, err := svcs.Automation.ProcessAutonomous(tctx, seeded.TenantID)
	if err != nil {
		return 1, fmt.Errorf("running the autonomous pass: %w", err)
	}
	stage("autonomous", "considered %d, executed %d without approval worth %s/month, skipped %d (%s)",
		autonomous.Considered, autonomous.Executed, autonomous.MonthlySaving.Format(),
		autonomous.Skipped, topSkipReason(autonomous.SkipReasons))

	// Pick the highest-saving recommendation the policy is willing to let a
	// human approve. Picking the highest-saving one outright would often land
	// on something the policy prohibits, and a demo that ends in "prohibited"
	// demonstrates the guard but not the flow.
	rec, decision, err := pickApprovableRecommendation(tctx, application, seeded.TenantID)
	if err != nil {
		return 1, err
	}
	stage("selected", "%s (%s/month, %s risk, policy says %s)",
		rec.Title, rec.EstimatedMonthlySaving.Format(), rec.Risk.Level, decision.Effect)

	costBefore := application.Estate.TotalMonthlyCost()
	stage("estate cost before", "%s/month", costBefore.Format())

	plan, err := svcs.Automation.PlanExecution(tctx, seeded.TenantID, rec.ID, ports.PlanOptions{RequestedBy: "coptx"})
	if err != nil {
		return 1, fmt.Errorf("planning execution: %w", err)
	}
	rollbackSteps := 0
	if plan.Rollback != nil {
		rollbackSteps = len(plan.Rollback.Steps)
	}
	stage("planned", "plan %s: %d step(s), %d rollback step(s), state %s",
		plan.ID, len(plan.Steps), rollbackSteps, plan.State)

	if plan.State == execute.PlanAwaitingApproval {
		requests, err := application.Repositories.Approvals.ListBySubject(
			tctx, seeded.TenantID, govern.SubjectExecutionPlan, plan.ID)
		if err != nil || len(requests) == 0 {
			// Fall back to the recommendation as the approval subject; which
			// one a plan's approval hangs off is an implementation detail of
			// the automation service, and the demo should not break if it
			// changes.
			requests, err = application.Repositories.Approvals.ListBySubject(
				tctx, seeded.TenantID, govern.SubjectRecommendation, rec.ID)
			if err != nil {
				return 1, fmt.Errorf("finding the approval request: %w", err)
			}
		}
		if len(requests) == 0 {
			return 1, fmt.Errorf("plan %s awaits approval but no approval request was created", plan.ID)
		}
		granted, err := svcs.Governance.Decide(tctx, seeded.TenantID, requests[0].ID, govern.Response{
			Principal: "demo-approver@shopfleet.example",
			Role:      core.RoleTenantAdmin,
			Approved:  true,
			Comment:   "Approved for the demo run.",
			At:        time.Now().UTC(),
		})
		if err != nil {
			return 1, fmt.Errorf("granting approval: %w", err)
		}
		stage("approved", "approval %s granted by %s", granted.ID, "demo-approver@shopfleet.example")
	} else {
		stage("approved", "policy permitted autonomous execution; no human approval needed")
	}

	executed, err := svcs.Automation.Execute(tctx, seeded.TenantID, plan.ID, "coptx")
	if err != nil {
		return 1, fmt.Errorf("executing plan %s: %w", plan.ID, err)
	}
	stage("executed", "plan %s is %s", executed.ID, executed.State)

	costAfter := application.Estate.TotalMonthlyCost()
	delta := costBefore.MustSub(costAfter)
	stage("estate cost after", "%s/month (down %s)", costAfter.Format(), delta.Format())

	validation, err := svcs.Automation.Validate(tctx, seeded.TenantID, plan.ID)
	if err != nil {
		return 1, fmt.Errorf("validating plan %s: %w", plan.ID, err)
	}
	stage("validated", "verdict %s across %d check(s); rolled back: %v",
		validation.Verdict, len(validation.Checks), validation.RollbackTriggered)

	funnel, err := svcs.Automation.Funnel(tctx, seeded.TenantID, core.PeriodOfDays(time.Now().UTC(), 30))
	if err != nil {
		return 1, fmt.Errorf("computing the savings funnel: %w", err)
	}
	stage("savings funnel", "potential %s -> approved %s -> executed %s -> validated %s -> realized %s",
		funnel.Potential.Format(), funnel.Approved.Format(), funnel.Executed.Format(),
		funnel.Validated.Format(), funnel.Realized.Format())

	summary, err := svcs.Economics.ExecutiveSummary(tctx, seeded.TenantID)
	if err != nil {
		return 1, fmt.Errorf("building the executive summary: %w", err)
	}
	stage("executive summary", "spend %s, potential savings %s, realized %s, efficiency %s (%.0f)",
		summary.MonthlySpend.Format(), summary.PotentialSavings.Format(),
		summary.RealizedSavings.Format(), summary.EfficiencyGrade, summary.EfficiencyScore)

	answer, err := svcs.Copilot.Ask(tctx, seeded.TenantID, ports.CopilotRequest{
		Question: "What is driving our biggest cost, and what should we do about it?",
		Actor:    "coptx",
	})
	if err != nil {
		return 1, fmt.Errorf("asking the copilot: %w", err)
	}
	stage("copilot", "grounded=%v, %d citation(s): %s",
		answer.Grounded, len(answer.Citations), truncateLine(answer.Answer, 120))

	if *asJSON {
		return 0, writeJSON(os.Stdout, out)
	}
	fmt.Println()
	return 0, nil
}

// pickApprovableRecommendation returns the highest-saving open
// recommendation whose policy decision permits execution, along with that
// decision.
func pickApprovableRecommendation(ctx context.Context, application *app.App, tenant core.TenantID) (optimize.Recommendation, govern.Decision, error) {
	page, err := application.Services.Optimization.List(ctx, tenant,
		ports.RecommendationFilter{Statuses: []optimize.Status{optimize.StatusOpen}},
		ports.ListOptions{Limit: 500})
	if err != nil {
		return optimize.Recommendation{}, govern.Decision{}, fmt.Errorf("listing recommendations: %w", err)
	}

	var best optimize.Recommendation
	var bestDecision govern.Decision
	for _, rec := range page.Items {
		if !rec.EstimatedMonthlySaving.GreaterThan(best.EstimatedMonthlySaving) {
			continue
		}
		decision, err := application.Services.Governance.Evaluate(ctx, tenant, rec.ID)
		if err != nil {
			continue
		}
		if !decision.Allowed() {
			continue
		}
		best, bestDecision = rec, decision
	}
	if best.ID.IsZero() {
		return optimize.Recommendation{}, govern.Decision{},
			fmt.Errorf("no open recommendation is permitted to execute under the active policy")
	}
	return best, bestDecision, nil
}

// topSkipReason names the most common reason the autonomous pass declined
// to act, so the stage line says why a skip happened rather than only that
// one did. Ties break alphabetically so the demo's output is reproducible
// across runs rather than dependent on map iteration order.
func topSkipReason(reasons map[string]int) string {
	if len(reasons) == 0 {
		return "none"
	}
	best, bestN := "", -1
	for reason, n := range reasons {
		if n > bestN || (n == bestN && reason < best) {
			best, bestN = reason, n
		}
	}
	return fmt.Sprintf("most often: %s x%d", best, bestN)
}

func dashes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}

func truncateLine(s string, n int) string {
	for i, r := range s {
		if r == '\n' {
			s = s[:i]
			break
		}
	}
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
