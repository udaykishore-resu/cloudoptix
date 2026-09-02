package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
	"github.com/udaykishore-resu/cloudoptix/policies"
)

// DemoTenantID, DemoTenantSlug and DemoTenantName identify the seeded demo
// tenant.
//
// The id is a fixed string rather than a minted core.ID because two other
// things have to agree with it before Seed has ever run: the development
// static-token issuer, which mints a principal scoped to a tenant at process
// start (see buildAuthenticator), and any script or test that wants to call
// the API without first parsing a seed's output. Onboarding accepts a
// caller-supplied tenant scope for exactly this case
// (ports.StartOnboardingInput.ExistingTenant), so nothing here bypasses the
// normal creation path to get a predictable id.
const (
	DemoTenantID   core.TenantID = "shopfleet-demo"
	DemoTenantSlug               = "shopfleet-demo"
	DemoTenantName               = "ShopFleet"
	demoActor                    = "demo@shopfleet.example"
)

// demoConversation is the onboarding conversation the demo tenant is created
// from. It is a real conversation driven through the real
// ports.OnboardingService against the deterministic provider — not a
// pre-baked spec.Spec written straight to the repository.
//
// That matters for a reason beyond tidiness: the specification's provenance
// map records, per field, whether a value was stated by the user or inferred
// by the agent. A hand-written spec would carry no provenance at all, and
// every screen that shows "we inferred this, confirm it?" would be empty in
// the demo — hiding the single feature onboarding exists to provide.
var demoConversation = []string{
	"We are ShopFleet, a retail company. We're a mid-size company operating across North America and Europe.",
	"Our platform is called Storefront, an online retail marketplace built as microservices " +
		"on EKS, ECS and Lambda. We use PostgreSQL and DynamoDB, Redis for caching, and SQS and SNS for messaging.",
	"Please analyse AWS account 412984773301 in us-east-1, that's our production environment.",
	"We handle about 900,000 checkouts per month, 2,400,000 searches per month, " +
		"860,000 payments per month and 1,500,000 logins per month. We need to meet PCI-DSS and SOC2.",
	"We want to cut costs by 25% and our monthly budget is $150,000. " +
		"Our availability target is 99.95% and max latency should stay under 300ms.",
	"We have a medium risk tolerance for optimization changes, spot instances are fine for " +
		"non-critical workloads, and production changes always require human approval.",
}

// SeedResult is what Seed produced, both for the printed summary and for the
// end-to-end tests, which assert on these numbers rather than re-deriving
// them.
type SeedResult struct {
	TenantID   core.TenantID
	AccountID  core.ID
	AlreadyRan bool

	SpecVersion    int
	SpecCompletion float64
	InferredFields int

	ResourcesDiscovered int
	RelationshipsFound  int
	MetricsCollected    int

	CostRecords  int
	MonthlySpend core.Money
	EstateCost   core.Money

	TwinNodes int
	TwinEdges int

	Transactions        int
	CostPerCheckout     core.Money
	AttributedCost      core.Money
	AttributionCoverage float64

	CostSLOs int
	Policy   string

	Recommendations int
	// Alternatives is how many of Recommendations are mutually exclusive
	// alternatives to another recommendation and therefore excluded from
	// MonthlySaving. Printing it beside the headline is what makes the
	// headline defensible: it says out loud that the platform found more
	// ways to fix things than it counted money for.
	Alternatives      int
	MonthlySaving     core.Money
	AnnualSaving      core.Money
	SavingByCategory  map[optimize.Category]core.Money
	CountByCategory   map[optimize.Category]int
	AutoExecutable    int
	RequiringApproval int
	Prohibited        int
	TopOpportunities  []optimize.Recommendation
}

// Seed creates the demo tenant end to end and returns what it produced.
//
// It is idempotent by tenant slug: a second call against a store that
// already holds the demo tenant returns the existing state with
// AlreadyRan set rather than creating a second tenant or a second
// conversation. Idempotence is what makes `--seed-demo` safe to leave on in
// a deployment that restarts, and what makes `coptx demo seed` safe to run
// twice while writing a demo script.
func Seed(ctx context.Context, app *App) (*SeedResult, error) {
	if app.Estate == nil {
		return nil, errors.New("app: the demo seed requires aws.mode=simulated — " +
			"seeding a live account would mean writing a fictional estate into a real customer's tenant")
	}

	// The idempotency check is a lookup by slug before any tenant scope
	// exists to hold, so it needs the one identity permitted to read across
	// tenants. TenantRepository.GetBySlug guards on the tenant it finds, and
	// a system principal scoped to "" would be refused by that guard — which
	// is the guard working correctly, not something to route around with a
	// direct map read.
	lookup := core.SystemPrincipal("", "demo-seed")
	lookup.Roles = append(lookup.Roles, core.RolePlatformAdmin)
	if existing, err := app.Repositories.Tenants.GetBySlug(core.WithPrincipal(ctx, lookup), DemoTenantSlug); err == nil {
		return summarizeExisting(ctx, app, existing)
	}

	res := &SeedResult{TenantID: DemoTenantID}

	if err := seedOnboarding(ctx, app, res); err != nil {
		return nil, err
	}

	// Every step past onboarding runs as the demo tenant's own admin, not as
	// a cross-tenant system principal: the seed exercises the same
	// tenant-guarded paths a real user's requests take, so a tenant-scoping
	// bug shows up here rather than only in production.
	tctx := core.WithPrincipal(ctx, core.Principal{
		Subject: demoActor, TenantID: DemoTenantID, Email: demoActor,
		Roles: []core.Role{core.RoleTenantAdmin}, IssuedAt: time.Now().UTC(),
	})

	if err := seedAccount(tctx, app, res); err != nil {
		return nil, err
	}
	if err := seedAutomationRevision(tctx, app, res); err != nil {
		return nil, err
	}
	if err := seedTopology(tctx, app, res); err != nil {
		return nil, err
	}
	if err := seedDiscovery(tctx, app, res); err != nil {
		return nil, err
	}
	if err := seedCost(tctx, app, res); err != nil {
		return nil, err
	}
	if err := seedTwin(tctx, app, res); err != nil {
		return nil, err
	}
	if err := seedEconomics(tctx, app, res); err != nil {
		return nil, err
	}
	if err := seedPolicy(tctx, app, res); err != nil {
		return nil, err
	}
	if err := seedOptimization(tctx, app, res); err != nil {
		return nil, err
	}

	res.EstateCost = app.Estate.TotalMonthlyCost()
	return res, nil
}

// seedOnboarding drives the conversation to a complete specification and
// approves it, which is what creates the tenant.
func seedOnboarding(ctx context.Context, app *App, res *SeedResult) error {
	svc := app.Services.Onboarding

	state, err := svc.Start(ctx, ports.StartOnboardingInput{
		Actor: demoActor, ActorEmail: demoActor,
		InitialMessage: demoConversation[0],
		ExistingTenant: DemoTenantID,
	})
	if err != nil {
		return fmt.Errorf("demo seed: starting onboarding: %w", err)
	}
	for _, msg := range demoConversation[1:] {
		if state, err = svc.Send(ctx, state.ConversationID, msg); err != nil {
			return fmt.Errorf("demo seed: onboarding turn %q: %w", truncate(msg, 40), err)
		}
	}

	summary, err := svc.Summarize(ctx, state.ConversationID)
	if err != nil {
		return fmt.Errorf("demo seed: summarizing onboarding: %w", err)
	}
	if !summary.CanApprove {
		return fmt.Errorf("demo seed: the demo conversation did not reach an approvable specification: %s",
			strings.Join(summary.BlockingReasons, "; "))
	}

	result, err := svc.Approve(ctx, ports.ApproveOnboardingInput{
		ConversationID: state.ConversationID,
		Actor:          demoActor, ActorEmail: demoActor,
		TenantName: DemoTenantName, TenantSlug: DemoTenantSlug,
		Plan: tenancy.PlanStandard, Demo: true,
	})
	if err != nil {
		return fmt.Errorf("demo seed: approving the specification: %w", err)
	}

	res.TenantID = result.Tenant.ID
	res.SpecVersion = result.SpecVersion.Version
	res.SpecCompletion = summary.Completeness.Score
	res.InferredFields = len(state.Inferred)
	return nil
}

// seedAutomationRevision records the connected access mode and enables
// automation, as a second reviewed specification version rather than during
// onboarding.
//
// This is not a workaround for something onboarding cannot express — it is
// the order the platform's own validation insists on, and it is the right
// order. spec.Spec.Validate refuses to approve a specification that switches
// automation on against a production account without also declaring a
// maintenance window, and a maintenance window is a schedule, not a fact a
// conversation extracts from prose. It equally refuses an assume_role
// specification with no role ARN, which no conversation can supply either:
// the ARN does not exist until the customer has actually connected the
// account. So the demo tenant onboards with automation off (the safe
// default), connects its account, and only then proposes a revision that
// states how the account is really reached and turns automation on together
// with the window, the validation period and the automatic rollback that
// make it safe. The diff between v1 and v2 is exactly the change a reviewer
// would want to see.
func seedAutomationRevision(ctx context.Context, app *App, res *SeedResult) error {
	revision, err := app.Services.Specs.ProposeRevision(ctx, res.TenantID, map[string]any{
		// The estate is the in-repo simulator, and the specification says so
		// rather than claiming an assume_role connection that does not exist.
		"aws.accessMode":                     string(cloud.AccessSimulated),
		"security.awsAccessMode":             string(cloud.AccessSimulated),
		"automation.enabled":                 true,
		"automation.autoRollback":            true,
		"automation.environments":            []string{"staging", "development", "sandbox"},
		"automation.maxConcurrentChanges":    3,
		"automation.maxMonthlyImpact":        25_000,
		"automation.validationWindowMinutes": 60,
		"automation.maintenanceWindows": []map[string]any{{
			"name":            "overnight-utc",
			"days":            []string{"tuesday", "wednesday", "thursday"},
			"startUtc":        "02:00",
			"durationMinutes": 240,
			"environments":    []string{"staging", "production"},
		}, {
			// The development sandbox has no change window, and saying so is
			// the point rather than a convenience. A maintenance window exists
			// to protect a schedule someone else depends on; a sandbox has no
			// such schedule, which is what makes it the environment the
			// platform is allowed to act in unattended. Declaring the window
			// as "always" is how that gets stated in the vocabulary the
			// automation loop reads, instead of leaving development out of
			// every window and letting the loop silently never fire.
			"name":            "development-anytime",
			"days":            []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"},
			"startUtc":        "00:00",
			"durationMinutes": 1440,
			"environments":    []string{"development", "sandbox"},
		}},
	}, demoActor)
	if err != nil {
		return fmt.Errorf("demo seed: proposing the automation revision: %w", err)
	}
	approved, err := app.Services.Specs.Approve(ctx, res.TenantID, revision.ID, demoActor)
	if err != nil {
		return fmt.Errorf("demo seed: approving the automation revision: %w", err)
	}
	res.SpecVersion = approved.Version
	return nil
}

// seedAccount registers and verifies the simulated AWS account.
func seedAccount(ctx context.Context, app *App, res *SeedResult) error {
	account, _, err := app.Services.AWSAccounts.Register(ctx, res.TenantID, ports.RegisterAccountInput{
		AccountID:   app.Estate.AccountID,
		Alias:       app.Estate.Alias,
		Environment: core.EnvProduction,
		Regions:     app.Estate.Regions,
		AccessMode:  cloud.AccessSimulated,
		IsPayer:     true,
	})
	if err != nil {
		return fmt.Errorf("demo seed: registering AWS account %s: %w", app.Estate.AccountID, err)
	}
	verified, check, err := app.Services.AWSAccounts.Verify(ctx, res.TenantID, account.ID)
	if err != nil {
		return fmt.Errorf("demo seed: verifying AWS account %s: %w", app.Estate.AccountID, err)
	}
	if !check.Reachable || len(verified.GrantedScopes) == 0 {
		return fmt.Errorf("demo seed: simulated account %s verified as unreachable", app.Estate.AccountID)
	}
	res.AccountID = verified.ID
	return nil
}

// demoApplication describes one application the demo estate's tags already
// carry, plus the workloads underneath it.
type demoApplication struct {
	tag         string
	name        string
	criticality core.Criticality
	team        string
	workloads   []string
}

// demoApplications mirrors the Application tag values awssim.BuildDemoEstate
// stamps on its resources. They are declared here, before discovery runs,
// because attribution is applied during the scan: an application created
// afterwards would leave every already-discovered resource unattributed
// until the next scan, and every economics figure would read zero in the
// meantime.
func demoApplications() []demoApplication {
	return []demoApplication{
		{"checkout-api", "Checkout API", core.CriticalityTier0, "payments",
			[]string{"checkout-api", "payments-svc"}},
		{"web-storefront", "Web Storefront", core.CriticalityTier0, "storefront",
			[]string{"web-storefront"}},
		{"catalog-service", "Catalog Service", core.CriticalityTier1, "catalog",
			[]string{"catalog-service"}},
		{"search", "Search", core.CriticalityTier1, "search",
			[]string{"search-query", "search-indexer"}},
		{"inventory", "Inventory", core.CriticalityTier1, "fulfillment",
			[]string{"inventory-svc"}},
		{"order-fulfillment", "Order Fulfillment", core.CriticalityTier1, "fulfillment",
			[]string{"order-fulfillment"}},
		{"notifications", "Notifications", core.CriticalityTier2, "platform",
			[]string{"notifications-worker"}},
		{"recommendation-engine", "Recommendation Engine", core.CriticalityTier2, "data",
			[]string{"recommendation-engine"}},
		{"analytics-batch", "Analytics Batch", core.CriticalityTier3, "data",
			[]string{"analytics-batch"}},
		{"admin-portal", "Admin Portal", core.CriticalityTier3, "platform",
			[]string{"admin-portal"}},
	}
}

// seedTopology creates the applications and workloads discovery attributes
// resources to.
func seedTopology(ctx context.Context, app *App, res *SeedResult) error {
	now := time.Now().UTC()
	for i, da := range demoApplications() {
		appID := core.NewID("app")
		if err := app.Repositories.Applications.UpsertApplication(ctx, cloud.Application{
			ID: appID, TenantID: res.TenantID, Name: da.name, Slug: da.tag,
			Domain: "ecommerce", Criticality: da.criticality, Owner: da.team,
			Environments: []core.Environment{core.EnvProduction, core.EnvStaging},
			MatchRules: []cloud.AttributionRule{{
				Priority: 10 + i, TagKey: "Application", TagValue: da.tag,
				Note: "Application tag stamped by the team's own Terraform",
			}},
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return fmt.Errorf("demo seed: creating application %q: %w", da.name, err)
		}

		for j, wl := range da.workloads {
			// The workload rule is a name prefix rather than a tag: several
			// workloads share one Application tag, and the resource names
			// are the only thing in the estate that tells them apart. A
			// tag-only rule would collapse every workload of an application
			// into one, and the per-workload footprint — the finest
			// granularity economics reports — would be meaningless.
			if err := app.Repositories.Applications.UpsertWorkload(ctx, cloud.Workload{
				ID: core.NewID("wl"), TenantID: res.TenantID, ApplicationID: appID,
				Name: wl, Type: cloud.WorkloadMicroservice, Platform: cloud.PlatformEC2,
				Environment: core.EnvProduction, Criticality: da.criticality, Team: da.team,
				MatchRules: []cloud.AttributionRule{{
					Priority: 100 + i*10 + j, TagKey: "Application", TagValue: da.tag,
					NamePrefix: wl, Note: "resource name prefix identifies the workload",
				}},
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return fmt.Errorf("demo seed: creating workload %q: %w", wl, err)
			}
		}
	}
	return nil
}

// seedDiscovery scans the simulated estate, inventory and metrics together.
func seedDiscovery(ctx context.Context, app *App, res *SeedResult) error {
	run, err := app.Services.Discovery.Run(ctx, res.TenantID, ports.DiscoveryRequest{
		AccountID: res.AccountID, Trigger: "onboarding", IncludeMetrics: true,
	})
	if err != nil {
		return fmt.Errorf("demo seed: running discovery: %w", err)
	}
	if run.ResourcesDiscovered == 0 {
		return fmt.Errorf("demo seed: discovery found no resources (state %s, errors %v)", run.State, run.Errors)
	}
	res.ResourcesDiscovered = run.ResourcesDiscovered
	res.RelationshipsFound = run.RelationshipsFound
	res.MetricsCollected = run.MetricsCollected
	return nil
}

// demoCostDays is the history the seed ingests. Ninety days is not
// arbitrary: the anomaly detector builds a trailing baseline, the forecaster
// selects its method from the amount of history available, and the
// month-over-month figures on the executive summary need a prior month to
// compare against. A shorter window would leave all three degraded and the
// demo would show empty charts.
const demoCostDays = 90

func seedCost(ctx context.Context, app *App, res *SeedResult) error {
	period := core.PeriodOfDays(time.Now().UTC(), demoCostDays)
	ingest, err := app.Services.Costs.Ingest(ctx, res.TenantID, res.AccountID, period)
	if err != nil {
		return fmt.Errorf("demo seed: ingesting %d days of cost: %w", demoCostDays, err)
	}
	res.CostRecords = ingest.RecordsIngested

	summary, err := app.Services.Costs.Summary(ctx, res.TenantID, core.PeriodOfDays(time.Now().UTC(), 30))
	if err != nil {
		return fmt.Errorf("demo seed: summarizing cost: %w", err)
	}
	res.MonthlySpend = summary.Total

	if _, err := app.Services.Costs.DetectAnomalies(ctx, res.TenantID, period); err != nil {
		return fmt.Errorf("demo seed: detecting cost anomalies: %w", err)
	}
	return nil
}

func seedTwin(ctx context.Context, app *App, res *SeedResult) error {
	stats, err := app.Services.Twin.Rebuild(ctx, res.TenantID)
	if err != nil {
		return fmt.Errorf("demo seed: building the architecture twin: %w", err)
	}
	res.TwinNodes = stats.NodeCount
	res.TwinEdges = stats.EdgeCount
	return nil
}

// demoTransaction declares one business transaction and the workloads on its
// critical path.
type demoTransaction struct {
	name        string
	description string
	volume      float64
	workloads   []string
	criticality core.Criticality
}

func demoTransactions() []demoTransaction {
	return []demoTransaction{
		{"checkout", "A customer completes an order", 900_000,
			[]string{"checkout-api", "payments-svc", "inventory-svc", "catalog-service"}, core.CriticalityTier0},
		{"search", "A customer searches the catalog", 2_400_000,
			[]string{"search-query", "search-indexer", "catalog-service"}, core.CriticalityTier1},
		{"payment", "A payment is authorized and captured", 860_000,
			[]string{"payments-svc", "checkout-api"}, core.CriticalityTier0},
		{"login", "A customer authenticates", 1_500_000,
			[]string{"web-storefront", "notifications-worker"}, core.CriticalityTier2},
	}
}

// seedEconomics defines the business transactions, computes footprints and
// declares the cost SLOs.
func seedEconomics(ctx context.Context, app *App, res *SeedResult) error {
	workloadIDs, err := workloadIndex(ctx, app, res.TenantID)
	if err != nil {
		return err
	}

	for _, dt := range demoTransactions() {
		ids := make([]core.ID, 0, len(dt.workloads))
		for _, name := range dt.workloads {
			if id, ok := workloadIDs[name]; ok {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return fmt.Errorf("demo seed: transaction %q names no workload that exists", dt.name)
		}
		// PathShare is left unset: economics splits evenly when it is
		// absent, and inventing precise shares here would present a guess as
		// a measurement. A real tenant supplies them from tracing.
		if _, err := app.Services.Economics.UpsertTransaction(ctx, econ.BusinessTransaction{
			TenantID: res.TenantID, Name: dt.name, Description: dt.description,
			WorkloadIDs: ids, Criticality: dt.criticality,
			VolumeSource: econ.VolumeSource{Kind: "declared", DeclaredMonthly: dt.volume},
			Provenance:   core.ProvenanceConfirmed,
		}); err != nil {
			return fmt.Errorf("demo seed: defining transaction %q: %w", dt.name, err)
		}
		res.Transactions++
	}

	period := core.PeriodOfDays(time.Now().UTC(), 30)
	econRes, err := app.Services.Economics.Compute(ctx, res.TenantID, period)
	if err != nil {
		return fmt.Errorf("demo seed: computing economics: %w", err)
	}
	res.AttributedCost = econRes.TotalAttributed
	res.AttributionCoverage = econRes.Coverage

	transactions, err := app.Services.Economics.ListTransactions(ctx, res.TenantID)
	if err != nil {
		return fmt.Errorf("demo seed: listing transactions: %w", err)
	}
	var checkoutID core.ID
	for _, t := range transactions {
		if t.Name == "checkout" {
			checkoutID = t.ID
		}
	}
	if !checkoutID.IsZero() {
		ue, err := app.Services.Economics.UnitEconomics(ctx, res.TenantID, checkoutID, period)
		if err != nil {
			return fmt.Errorf("demo seed: pricing the checkout transaction: %w", err)
		}
		res.CostPerCheckout = ue.CostPerUnit
	}

	slos := []econ.CostSLO{
		{
			TenantID: res.TenantID, Name: "Production infrastructure budget",
			Description: "Total production spend stays within the monthly budget the specification declares.",
			Kind:        econ.SLOAbsoluteSpend, Direction: econ.DirectionAtMost,
			Scope: econ.ScopeOrganization, Target: core.USDollars(150_000),
			Window: econ.WindowCalendarMonth, ErrorBudgetPct: 0.05,
			Owner: demoActor, Enabled: true,
		},
		{
			TenantID: res.TenantID, Name: "Cost per checkout",
			Description: "Infrastructure cost of one completed order.",
			Kind:        econ.SLOCostPerTransaction, Direction: econ.DirectionAtMost,
			Scope: econ.ScopeTransaction, TransactionID: checkoutID,
			Target: core.NewMoney(0.06, core.USD), Window: econ.WindowRolling30d, ErrorBudgetPct: 0.10,
			Owner: demoActor, Enabled: true,
		},
		{
			TenantID: res.TenantID, Name: "Waste ratio",
			Description: "The share of spend classified as identifiable waste.",
			Kind:        econ.SLOWasteRatio, Direction: econ.DirectionAtMost,
			Scope: econ.ScopeOrganization, TargetRatio: 0.10,
			Window: econ.WindowRolling30d, ErrorBudgetPct: 0.20,
			Owner: demoActor, Enabled: true,
		},
	}
	for _, s := range slos {
		if s.Kind == econ.SLOCostPerTransaction && s.TransactionID.IsZero() {
			continue
		}
		if _, err := app.Services.Economics.UpsertCostSLO(ctx, s); err != nil {
			return fmt.Errorf("demo seed: declaring cost SLO %q: %w", s.Name, err)
		}
		res.CostSLOs++
	}
	if _, err := app.Services.Economics.EvaluateSLOs(ctx, res.TenantID); err != nil {
		return fmt.Errorf("demo seed: evaluating cost SLOs: %w", err)
	}
	return nil
}

// workloadIndex maps workload name to id across every application.
func workloadIndex(ctx context.Context, app *App, tenant core.TenantID) (map[string]core.ID, error) {
	apps, err := app.Repositories.Applications.ListApplications(ctx, tenant)
	if err != nil {
		return nil, fmt.Errorf("demo seed: listing applications: %w", err)
	}
	out := map[string]core.ID{}
	for _, a := range apps {
		wls, err := app.Repositories.Applications.ListWorkloads(ctx, tenant, a.ID)
		if err != nil {
			return nil, fmt.Errorf("demo seed: listing workloads of %q: %w", a.Name, err)
		}
		for _, w := range wls {
			out[w.Name] = w.ID
		}
	}
	return out, nil
}

// seedPolicy activates the shipped balanced pack, replacing the minimal
// "baseline" policy onboarding's approval creates.
func seedPolicy(ctx context.Context, app *App, res *SeedResult) error {
	data, err := policies.FS.ReadFile("balanced.yaml")
	if err != nil {
		return fmt.Errorf("demo seed: reading the balanced policy pack: %w", err)
	}
	policy, err := app.services.governance.LoadPolicyYAML(ctx, res.TenantID, data, demoActor)
	if err != nil {
		return fmt.Errorf("demo seed: loading the balanced policy pack: %w", err)
	}
	if err := app.Services.Governance.ActivatePolicy(ctx, res.TenantID, policy.ID, demoActor); err != nil {
		return fmt.Errorf("demo seed: activating the balanced policy pack: %w", err)
	}
	res.Policy = policy.Name
	return nil
}

// seedOptimization runs the rule set and records what it found.
func seedOptimization(ctx context.Context, app *App, res *SeedResult) error {
	result, err := app.Services.Optimization.Analyze(ctx, res.TenantID, ports.AnalyzeRequest{})
	if err != nil {
		return fmt.Errorf("demo seed: running the optimization analysis: %w", err)
	}
	res.Recommendations = result.RecommendationsCreated
	res.Alternatives = result.MutuallyExclusiveAlternatives
	res.MonthlySaving = result.TotalMonthlySaving
	res.AnnualSaving = result.TotalAnnualSaving
	res.AutoExecutable = result.AutoExecutable
	res.RequiringApproval = result.RequiringApproval
	res.Prohibited = result.Prohibited

	// The rule engine deliberately leaves RequiresApproval, AutoExecutable
	// and PolicyDecisionID unset (see optimize.Recommendation): whether a
	// change may run is the policy engine's call, not a rule's. So the seed
	// routes every open recommendation through the policy engine here, which
	// is also what makes the recommendation list render its governance
	// column instead of showing every item as ungoverned.
	open, err := listAllOpen(ctx, app, res.TenantID)
	if err != nil {
		return err
	}
	res.AutoExecutable, res.RequiringApproval, res.Prohibited = 0, 0, 0
	for _, rec := range open {
		decision, err := app.Services.Governance.Evaluate(ctx, res.TenantID, rec.ID)
		if err != nil {
			return fmt.Errorf("demo seed: evaluating policy for recommendation %s: %w", rec.ID, err)
		}
		switch decision.Effect {
		case govern.EffectAutoExecute:
			res.AutoExecutable++
		case govern.EffectRequireApproval:
			res.RequiringApproval++
		case govern.EffectProhibit:
			res.Prohibited++
		}
	}

	summary, err := app.Services.Optimization.Summary(ctx, res.TenantID)
	if err != nil {
		return fmt.Errorf("demo seed: summarizing recommendations: %w", err)
	}
	res.SavingByCategory = summary.SavingByCategory
	res.CountByCategory = summary.ByCategory
	res.TopOpportunities = topTen(open)
	return nil
}

// listAllOpen pages through every open recommendation. The repository's
// cursor pagination sorts by creation time for stability, not by priority
// (see the memstore and Postgres RecommendationRepository implementations),
// so "the top ten" has to be selected from the whole set rather than by
// asking for the first page of a priority sort that no repository actually
// offers.
func listAllOpen(ctx context.Context, app *App, tenant core.TenantID) ([]optimize.Recommendation, error) {
	var out []optimize.Recommendation
	opts := ports.ListOptions{Limit: 500}
	for {
		page, err := app.Services.Optimization.List(ctx, tenant,
			ports.RecommendationFilter{Statuses: []optimize.Status{optimize.StatusOpen}}, opts)
		if err != nil {
			return nil, fmt.Errorf("demo seed: listing recommendations: %w", err)
		}
		out = append(out, page.Items...)
		if page.NextCursor == "" || len(page.Items) == 0 {
			return out, nil
		}
		opts.Cursor = page.NextCursor
	}
}

// topTen ranks by the engine's own priority score, breaking ties on monthly
// saving so two equally-scored findings order by the thing a reader is
// actually comparing.
func topTen(recs []optimize.Recommendation) []optimize.Recommendation {
	ranked := append([]optimize.Recommendation(nil), recs...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].PriorityScore != ranked[j].PriorityScore {
			return ranked[i].PriorityScore > ranked[j].PriorityScore
		}
		return ranked[i].EstimatedMonthlySaving.GreaterThan(ranked[j].EstimatedMonthlySaving)
	})
	if len(ranked) > 10 {
		ranked = ranked[:10]
	}
	return ranked
}

// summarizeExisting rebuilds a SeedResult from an already-seeded tenant. It
// is what makes Seed idempotent: a second run reports the same summary
// without re-running discovery or creating a second conversation.
func summarizeExisting(ctx context.Context, app *App, t tenancy.Tenant) (*SeedResult, error) {
	tctx := core.WithPrincipal(ctx, core.Principal{
		Subject: demoActor, TenantID: t.ID, Email: demoActor,
		Roles: []core.Role{core.RoleTenantAdmin}, IssuedAt: time.Now().UTC(),
	})
	res := &SeedResult{
		TenantID: t.ID, AlreadyRan: true, SpecVersion: t.ActiveSpecVersion,
		EstateCost: app.Estate.TotalMonthlyCost(),
	}

	if accounts, err := app.Repositories.AWSAccounts.List(tctx, t.ID); err == nil && len(accounts) > 0 {
		res.AccountID = accounts[0].ID
	}
	if n, err := app.Repositories.Resources.Count(tctx, t.ID, ports.ResourceFilter{}); err == nil {
		res.ResourcesDiscovered = n
	}
	if summary, err := app.Services.Costs.Summary(tctx, t.ID, core.PeriodOfDays(time.Now().UTC(), 30)); err == nil {
		res.MonthlySpend = summary.Total
	}
	if txs, err := app.Services.Economics.ListTransactions(tctx, t.ID); err == nil {
		res.Transactions = len(txs)
	}
	if slos, err := app.Services.Economics.ListCostSLOs(tctx, t.ID); err == nil {
		res.CostSLOs = len(slos)
	}
	if p, err := app.Services.Governance.GetPolicy(tctx, t.ID); err == nil {
		res.Policy = p.Name
	}
	if s, err := app.Services.Optimization.Summary(tctx, t.ID); err == nil {
		res.Recommendations = s.Open
		res.MonthlySaving = s.TotalMonthlySaving
		res.AnnualSaving = s.TotalMonthlySaving.Annualized()
		res.SavingByCategory = s.SavingByCategory
		res.CountByCategory = s.ByCategory
		res.AutoExecutable = s.AutoExecutable
		res.RequiringApproval = s.AwaitingApproval
		res.Alternatives = s.MutuallyExclusiveAlternatives
	}
	if open, err := listAllOpen(tctx, app, t.ID); err == nil {
		res.TopOpportunities = topTen(open)
	}
	return res, nil
}

// PrintSummary renders the seed's result as the human-readable block the
// demo script reads out.
func (r *SeedResult) PrintSummary(w io.Writer) {
	if w == nil {
		w = os.Stdout
	}
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	p("")
	p("  CloudOptix demo tenant: %s (%s)", DemoTenantName, r.TenantID)
	if r.AlreadyRan {
		p("  (already seeded — reporting existing state)")
	}
	p("  %s", strings.Repeat("-", 68))
	p("  Specification         v%d, %.0f%% complete, %d inferred fields",
		r.SpecVersion, r.SpecCompletion*100, r.InferredFields)
	p("  Resources discovered  %d  (%d relationships, %d metric summaries)",
		r.ResourcesDiscovered, r.RelationshipsFound, r.MetricsCollected)
	p("  Monthly spend         %s   (simulated estate list price %s)",
		r.MonthlySpend.Format(), r.EstateCost.Format())
	p("  Architecture twin     %d nodes, %d edges", r.TwinNodes, r.TwinEdges)
	p("  Economics             %d transactions, %.0f%% of spend attributed",
		r.Transactions, r.AttributionCoverage*100)
	if !r.CostPerCheckout.IsZero() {
		p("  Cost per checkout     %s", r.CostPerCheckout.String())
	}
	p("  Cost SLOs             %d declared", r.CostSLOs)
	p("  Active policy         %s", r.Policy)
	p("  %s", strings.Repeat("-", 68))
	p("  Identified waste      %s/month  (%s/year)", r.MonthlySaving.Format(), r.AnnualSaving.Format())
	p("  Recommendations       %d  (%d auto-executable, %d need approval, %d prohibited)",
		r.Recommendations, r.AutoExecutable, r.RequiringApproval, r.Prohibited)
	if r.Alternatives > 0 {
		// Stated explicitly rather than left implicit: these are real
		// recommendations a user can choose, they are simply not additive
		// with the ones above them, so their savings are excluded from the
		// headline instead of inflating it.
		p("  Mutually exclusive    %d alternative(s) to a recommended fix, excluded from the total above",
			r.Alternatives)
	}

	if len(r.SavingByCategory) > 0 {
		p("")
		p("  By category:")
		type row struct {
			cat    optimize.Category
			amount core.Money
			count  int
		}
		rows := make([]row, 0, len(r.SavingByCategory))
		for cat, amount := range r.SavingByCategory {
			rows = append(rows, row{cat, amount, r.CountByCategory[cat]})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].amount.GreaterThan(rows[j].amount) })
		for _, rw := range rows {
			p("    %-24s %12s/month   %3d finding(s)", rw.cat, rw.amount.Format(), rw.count)
		}
	}

	if len(r.TopOpportunities) > 0 {
		p("")
		p("  Top %d opportunities:", len(r.TopOpportunities))
		for i, rec := range r.TopOpportunities {
			// An alternative's saving is printed but explicitly labelled, so
			// nobody reads this list as a column to add up: it is one of
			// several mutually exclusive ways to fix a problem another line
			// in this same list already counts.
			marker := ""
			if !rec.CountsTowardTotal() {
				marker = "  [alternative — not counted]"
			}
			p("   %2d. %-52s %11s/month  %s risk, %.0f%% confidence%s",
				i+1, truncate(rec.Title, 52), rec.EstimatedMonthlySaving.Format(),
				rec.Risk.Level, float64(rec.Confidence)*100, marker)
		}
	}
	p("")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
