package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/app"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestDemoFlow is the complete customer journey as one test: a conversation
// becomes a specification, a specification becomes a tenant, a tenant's
// estate is discovered, priced and modelled, the rules find waste, policy
// decides who may act on it, a human approves one change, the change
// executes against the estate, validation confirms it worked, and the
// savings funnel shows the money as realized.
//
// It is deliberately one test rather than nine. Every stage's input is the
// previous stage's output, and splitting them would either mean nine
// separate seeds (slow, and each one testing a fresh world rather than an
// accumulated one) or shared mutable state between tests, which is worse.
// Subtests keep the failure message specific about which stage broke.
func TestDemoFlow(t *testing.T) {
	a, seeded := seed(t)
	ctx := tenantCtx(seeded.TenantID)
	svcs := a.Services

	t.Run("onboarding produces a valid specification with realistic provenance", func(t *testing.T) {
		tenant, err := svcs.Tenants.Get(ctx, seeded.TenantID)
		require.NoError(t, err)
		assert.Equal(t, tenancy.StateActive, tenant.State)
		assert.Equal(t, app.DemoTenantSlug, tenant.Slug)
		assert.True(t, tenant.Demo, "the demo tenant must be flagged; it is the only gate on simulated AWS access")

		version, err := svcs.Specs.GetActive(ctx, seeded.TenantID)
		require.NoError(t, err)
		assert.Equal(t, spec.StatusApproved, version.Status)
		assert.NotEmpty(t, version.Checksum)

		s := version.Spec
		assert.Equal(t, "ShopFleet", s.Organization.Name)
		assert.Equal(t, "Storefront", s.Application.Name)
		require.Len(t, s.AWS.Accounts, 1)
		assert.Equal(t, string(a.Estate.AccountID), s.AWS.Accounts[0].ID)
		assert.InDelta(t, 0.25, s.Objectives.CostReductionTarget, 1e-9)
		assert.InDelta(t, 0.9995, s.Objectives.AvailabilityTarget, 1e-9)
		assert.NotEmpty(t, s.Business.Transactions, "the volumes stated in conversation must reach the specification")

		// Provenance is the point of conversational onboarding: a
		// specification whose every field reads CONFIRMED would mean the
		// agent inferred nothing, and one with no CONFIRMED fields would mean
		// it extracted nothing. Both are present here.
		require.NotEmpty(t, s.Provenance, "the specification must carry per-field provenance")
		var confirmed, inferred int
		for _, p := range s.Provenance {
			switch p {
			case core.ProvenanceConfirmed:
				confirmed++
			case core.ProvenanceInferred:
				inferred++
			}
		}
		assert.Greater(t, confirmed, 5, "fields the user actually stated must be marked CONFIRMED")
		assert.Greater(t, inferred, 0, "the agent must have inferred at least one field it was never told")
		assert.Equal(t, core.ProvenanceConfirmed, s.Provenance["organization.name"],
			"a name the user stated verbatim is confirmed, not inferred")
	})

	t.Run("discovery finds the estate", func(t *testing.T) {
		accounts, err := svcs.AWSAccounts.List(ctx, seeded.TenantID)
		require.NoError(t, err)
		require.Len(t, accounts, 1)
		assert.Equal(t, cloud.ConnConnected, accounts[0].State,
			"verification must have moved the account to connected: %s", accounts[0].StateReason)
		assert.Equal(t, cloud.AccessSimulated, accounts[0].AccessMode)
		// A simulated account carries no external id, and should not: the
		// external id is the confused-deputy guard on an AssumeRole trust
		// policy, and there is no trust policy here. Minting one anyway
		// would put a credential-shaped value on a record that has no
		// credential, which is how a "harmless" test fixture ends up in a
		// production trust policy.
		assert.Empty(t, accounts[0].ExternalID,
			"a simulated account has no AssumeRole trust policy, so it must carry no external id")

		status, err := svcs.Discovery.Status(ctx, seeded.TenantID)
		require.NoError(t, err)
		assert.Equal(t, 1, status.AccountsCovered)
		assert.Equal(t, seeded.ResourcesDiscovered, status.ResourceCount)

		// awssim.BuildDemoEstate is deterministic by design (its own tests
		// assert byte-identical totals across builds), so the discovered
		// count is asserted exactly rather than as a range: a scan that
		// silently stopped emitting one resource kind is precisely the
		// regression a range would hide. If the fixture's counts are
		// deliberately changed, this constant moves with them.
		// 870 production resources plus the 18-resource development sandbox
		// (awssim.buildDevelopmentSlice), which exists so the autonomous
		// path has something it is actually allowed to act on.
		const expectedResources = 888
		assert.Equal(t, expectedResources, seeded.ResourcesDiscovered,
			"discovery must find every resource the estate holds")
		assert.Greater(t, seeded.RelationshipsFound, 100, "the estate's topology must be discovered too")
		assert.Greater(t, seeded.MetricsCollected, 50, "utilisation must be collected for the resources that have any")
	})

	t.Run("cost reconciles with the estate", func(t *testing.T) {
		period := core.PeriodOfDays(time.Now().UTC(), 30)
		summary, err := svcs.Costs.Summary(ctx, seeded.TenantID, period)
		require.NoError(t, err)

		// The simulated cost ingestor rescales its generated daily records so
		// the period sums to the estate's own monthly total (see
		// awssim/cost.go). A 30-day window against a ~30.44-day month plus
		// the ingestor's day-boundary handling leaves a few percent of slack,
		// which is why this is a tolerance rather than equality — but it is a
		// tight one: a 10% drift would mean cost and inventory disagree about
		// the same estate.
		estate := a.Estate.TotalMonthlyCost()
		assert.InEpsilon(t, estate.Units(), summary.Total.Units(), 0.10,
			"ingested cost (%s) must reconcile with the estate's own monthly total (%s)",
			summary.Total.Format(), estate.Format())
		assert.False(t, summary.LastIngestedAt.IsZero())

		byService, err := svcs.Costs.Breakdown(ctx, seeded.TenantID,
			ports.CostFilter{Period: period}, "service")
		require.NoError(t, err)
		assert.Greater(t, len(byService.Items), 5, "a 30-service estate must break down across more than a handful")
	})

	t.Run("the twin graph is connected", func(t *testing.T) {
		graph, err := svcs.Twin.Graph(ctx, seeded.TenantID, ports.TwinQuery{View: "architecture"})
		require.NoError(t, err)
		require.NotEmpty(t, graph.Nodes)
		assert.NotEmpty(t, graph.Edges)

		// "Connected" here means the edges actually join nodes that exist —
		// a graph whose edges reference ids the node set does not contain
		// renders as a pile of orphans, and is the classic failure of a
		// topology built from two independently-filtered queries.
		ids := map[core.ID]bool{}
		for _, n := range graph.Nodes {
			ids[n.ID] = true
		}
		linked := map[core.ID]bool{}
		for _, e := range graph.Edges {
			assert.True(t, ids[e.From] || e.From == "", "edge references unknown node %s", e.From)
			assert.True(t, ids[e.To] || e.To == "", "edge references unknown node %s", e.To)
			linked[e.From] = true
			linked[e.To] = true
		}
		assert.GreaterOrEqual(t, len(linked), 2, "the graph must have at least one real connection")
		assert.False(t, graph.Stats.TotalCost.IsZero(), "the twin must carry the estate's cost")
	})

	t.Run("economics attributes cost and prices a checkout", func(t *testing.T) {
		assert.Equal(t, 4, seeded.Transactions)
		assert.GreaterOrEqual(t, seeded.AttributionCoverage, 0.80,
			"at least 80%% of spend must be attributable; below that the unit economics are guesswork")

		txs, err := svcs.Economics.ListTransactions(ctx, seeded.TenantID)
		require.NoError(t, err)
		var checkout econ.BusinessTransaction
		for _, tx := range txs {
			if tx.Name == "checkout" {
				checkout = tx
			}
		}
		require.NotEmpty(t, checkout.ID, "the checkout transaction must exist")
		require.NotEmpty(t, checkout.WorkloadIDs, "a transaction must name the workloads on its critical path")

		ue, err := svcs.Economics.UnitEconomics(ctx, seeded.TenantID, checkout.ID,
			core.PeriodOfDays(time.Now().UTC(), 30))
		require.NoError(t, err)
		// The declared figure is 900,000 per month; economics pro-rates it to
		// the requested window, so a 30-day window against a 30.44-day
		// average month lands slightly under. The tolerance covers exactly
		// that pro-rating and nothing wider — a volume that came from
		// somewhere other than the declaration would miss it by orders of
		// magnitude.
		assert.InEpsilon(t, 900_000.0, ue.Volume, 0.03,
			"volume comes from the declared figure in the specification, pro-rated to the window")

		// A sane cost per checkout for a $180K/month estate doing 900K
		// checkouts is cents, not dollars and not fractions of a cent. The
		// bound is wide because the attribution model is doing real work
		// here; it is bounded at all because an unbounded figure would let a
		// unit-conversion bug (micros read as dollars) pass unnoticed.
		cents := ue.CostPerUnit.Units()
		assert.Greater(t, cents, 0.0005, "cost per checkout of %s is implausibly low", ue.CostPerUnit.String())
		assert.Less(t, cents, 1.00, "cost per checkout of %s is implausibly high", ue.CostPerUnit.String())
	})

	t.Run("optimization finds roughly the estate's designed waste", func(t *testing.T) {
		designed := a.Estate.EstimatedIdentifiableWaste().Total
		found := seeded.MonthlySaving

		assert.Greater(t, seeded.Recommendations, 100, "a deliberately wasteful estate must yield real findings")

		// The estate's WasteBreakdown is a set of conservative heuristics,
		// not the rule engine's own output, so the two are expected to
		// differ — the engine also finds classes the breakdown does not model
		// (commitment gaps, Graviton migrations, Fargate-vs-EC2), and prices
		// several of the shared ones slightly differently. What is asserted
		// is that they land within a defensible distance of each other.
		//
		// The upper bound is the tighter half and it is deliberately tight.
		// Overlapping rules used to be summed — three answers to one
		// oversized node group, each counted in full — which put this ratio
		// above 1.5 and the headline above $65K against a designed $44K.
		// Conflict grouping (optimize/conflict.go) is what keeps it near 1.0,
		// and a bound of 2.0 would no longer notice that regression coming
		// back. The lower bound catches the opposite failure: a rule that
		// stopped firing, or grouping that started suppressing changes which
		// genuinely compose.
		ratio := found.Units() / designed.Units()
		assert.Greater(t, ratio, 0.85,
			"engine found %s against a designed %s — rules are not firing, or grouping is suppressing changes that compose",
			found.Format(), designed.Format())
		assert.Less(t, ratio, 1.35,
			"engine found %s against a designed %s — overlapping rules are being summed again",
			found.Format(), designed.Format())

		// Every category the estate was built to exercise must be
		// represented; a category at zero means that whole part of the story
		// is invisible.
		for _, cat := range []optimize.Category{
			optimize.CategoryRightsizing, optimize.CategoryWaste,
			optimize.CategoryStorage, optimize.CategoryArchitecture,
		} {
			assert.Greater(t, seeded.CountByCategory[cat], 0, "no findings in category %s", cat)
		}
		assert.Len(t, seeded.TopOpportunities, 10, "the summary reports the top ten opportunities")
	})

	t.Run("overlapping recommendations are grouped, not summed", func(t *testing.T) {
		open, err := listOpenRecommendations(ctx, a, seeded.TenantID)
		require.NoError(t, err)

		// The estate's EKS node groups are the worked example: three rules
		// each report a real saving on one node group — shrink the node
		// count, shrink the node size, shrink the pod requests that force the
		// node count — and at most one can be applied.
		groups := map[string][]optimize.Recommendation{}
		for _, r := range open {
			if r.ConflictGroupID != "" {
				groups[r.ConflictGroupID] = append(groups[r.ConflictGroupID], r)
			}
		}
		require.NotEmpty(t, groups, "this estate contains overlapping findings; none were grouped")

		for id, members := range groups {
			require.Greater(t, len(members), 1, "group %s has one member, which is not a conflict", id)
			primaries := 0
			for _, m := range members {
				assert.True(t, m.MutuallyExclusive)
				assert.Len(t, m.AlternativeIDs, len(members)-1)
				if m.CountsTowardTotal() {
					primaries++
				}
			}
			assert.Equal(t, 1, primaries, "group %s must recommend exactly one of its members", id)
		}

		// The headline is the sum of the primaries and nothing else. Summing
		// every open recommendation is the bug this grouping exists to
		// prevent, so the test states both numbers and the gap between them.
		naive := core.ZeroUSD()
		for _, r := range open {
			naive = naive.MustAdd(r.EstimatedMonthlySaving)
		}
		counted := optimize.TotalPotentialSaving(open)
		assert.True(t, counted.LessThan(naive),
			"grouping suppressed nothing: naive sum %s, counted %s", naive.Format(), counted.Format())
		assert.Equal(t, counted.Micros(), seeded.MonthlySaving.Micros(),
			"the seed's headline must be the primaries-only total")
		assert.Equal(t, optimize.CountAlternatives(open), seeded.Alternatives)
	})

	t.Run("the unattended path executes without a human", func(t *testing.T) {
		// The demo estate carries a small development sandbox precisely so
		// this is demonstrable rather than theoretical: the balanced pack
		// auto-executes unambiguous waste in non-production, and an estate
		// that is entirely production could report "0 auto-executable"
		// forever while being completely correct.
		require.Greater(t, seeded.AutoExecutable, 0,
			"no recommendation is policy-cleared for unattended execution, so the autonomous path is untestable")
		require.Greater(t, seeded.RequiringApproval, 0,
			"the same run must also produce changes a human has to approve")

		result, err := svcs.Automation.ProcessAutonomous(ctx, seeded.TenantID)
		require.NoError(t, err)
		assert.Greater(t, result.Executed, 0,
			"the autonomous pass considered %d and executed none; skips: %v", result.Considered, result.SkipReasons)
		assert.Equal(t, 0, result.Failed)
		assert.False(t, result.MonthlySaving.IsZero())

		// Everything it touched must have been non-production, and none of it
		// destructive: those are the two properties the balanced pack's
		// autonomy rests on, and neither is enforced by the loop itself.
		plans, err := a.Repositories.Executions.ListPlans(ctx, seeded.TenantID,
			[]execute.PlanState{execute.PlanExecuted}, ports.ListOptions{Limit: 50})
		require.NoError(t, err)
		require.NotEmpty(t, plans.Items)
		for _, p := range plans.Items {
			assert.False(t, p.Environment.IsProduction(),
				"plan %s executed unattended against %s", p.ID, p.Environment)
			assert.False(t, p.Action.Destructive(),
				"plan %s executed a destructive action unattended", p.ID)
		}
	})

	t.Run("policy routes recommendations", func(t *testing.T) {
		policy, err := svcs.Governance.GetPolicy(ctx, seeded.TenantID)
		require.NoError(t, err)
		assert.Equal(t, "balanced", policy.Name)
		assert.Equal(t, govern.EffectRequireApproval, policy.DefaultEffect)

		routed := seeded.AutoExecutable + seeded.RequiringApproval + seeded.Prohibited
		assert.Greater(t, routed, 0, "the policy engine must have decided something")
		assert.Greater(t, seeded.RequiringApproval, 100,
			"the balanced pack requires approval for production changes, which is most of this estate")

		// Every decision is reproducible and recorded. Spot-check one: its
		// decision must be persisted, name the policy it came from, and carry
		// the digest that makes it replayable.
		rec := seeded.TopOpportunities[0]
		decision, err := svcs.Governance.Evaluate(ctx, seeded.TenantID, rec.ID)
		require.NoError(t, err)
		assert.Equal(t, policy.ID, decision.PolicyID)
		assert.NotEmpty(t, decision.InputDigest)
		assert.NotEmpty(t, decision.Reason)
		assert.Contains(t,
			[]govern.Effect{govern.EffectAutoExecute, govern.EffectRequireApproval,
				govern.EffectProhibit, govern.EffectAdvisory},
			decision.Effect)
	})

	// The remaining stages share one recommendation, so they run in sequence
	// against variables the outer function owns.
	var (
		chosen   optimize.Recommendation
		plan     execute.Plan
		costPre  core.Money
		costPost core.Money
	)

	t.Run("an approval is granted and a plan is built with a rollback", func(t *testing.T) {
		chosen = pickExecutable(t, ctx, a, seeded.TenantID)
		costPre = a.Estate.TotalMonthlyCost()

		var err error
		plan, err = svcs.Automation.PlanExecution(ctx, seeded.TenantID, chosen.ID,
			ports.PlanOptions{RequestedBy: "e2e"})
		require.NoError(t, err)

		require.NotNil(t, plan.Rollback, "a plan without a rollback must never be built")
		assert.True(t, plan.Rollback.Feasible)
		assert.NotEmpty(t, plan.Rollback.Steps)
		assert.NotEmpty(t, plan.Snapshots, "a plan that mutates must capture a snapshot first")

		// The plan must not be executable before approval; that is the whole
		// governance mechanism, and asserting it here is what proves the
		// state machine, not just the policy engine, enforces it.
		ok, reason := plan.Executable(time.Now().UTC())
		assert.False(t, ok, "an unapproved plan must not report itself executable")
		assert.Contains(t, reason, string(plan.State))

		requests, err := a.Repositories.Approvals.ListBySubject(ctx, seeded.TenantID,
			govern.SubjectExecutionPlan, plan.ID)
		require.NoError(t, err)
		require.NotEmpty(t, requests, "a plan awaiting approval must have raised an approval request")

		granted, err := svcs.Governance.Decide(ctx, seeded.TenantID, requests[0].ID, govern.Response{
			Principal: "approver@shopfleet.example",
			Role:      core.RoleTenantAdmin,
			Approved:  true,
			Comment:   "Approved by the end-to-end test.",
			At:        time.Now().UTC(),
		})
		require.NoError(t, err)
		assert.Equal(t, govern.ApprovalApproved, granted.State)
	})

	t.Run("execution mutates the simulated estate", func(t *testing.T) {
		executed, err := svcs.Automation.Execute(ctx, seeded.TenantID, plan.ID, "e2e")
		require.NoError(t, err)
		require.Equal(t, execute.PlanExecuted, executed.State,
			"plan finished as %s: %s", executed.State, executed.StateReason)

		for _, step := range executed.Steps {
			assert.NotEqual(t, execute.StepFailed, step.State,
				"step %q failed: %s", step.Name, step.Error)
		}

		costPost = a.Estate.TotalMonthlyCost()
		assert.True(t, costPost.LessThan(costPre),
			"the estate cost %s before and %s after — execution changed nothing",
			costPre.Format(), costPost.Format())

		// The saving is not merely "some reduction": the executor changed
		// exactly what the recommendation proposed, so the estate's cost must
		// fall by the predicted amount. This is the assertion that
		// distinguishes "a change happened" from "the right change happened".
		actual := costPre.MustSub(costPost)
		assert.InEpsilon(t, chosen.EstimatedMonthlySaving.Units(), actual.Units(), 0.02,
			"predicted %s, estate fell by %s", chosen.EstimatedMonthlySaving.Format(), actual.Format())
	})

	t.Run("validation passes", func(t *testing.T) {
		result, err := svcs.Automation.Validate(ctx, seeded.TenantID, plan.ID)
		require.NoError(t, err)
		assert.Equal(t, execute.VerdictSuccess, result.Verdict,
			"validation verdict %s: %s", result.Verdict, result.Explanation)
		assert.False(t, result.RollbackTriggered)
		require.NotEmpty(t, result.Checks, "a validation with no checks proves nothing")
		for _, check := range result.Checks {
			assert.True(t, check.Passed, "check %q failed: %s", check.Name, check.Detail)
		}
		assert.False(t, result.ObservedMonthlySaving.IsZero(),
			"the realized saving must be measured, not assumed")
	})

	t.Run("the savings funnel shows realized savings", func(t *testing.T) {
		funnel, err := svcs.Automation.Funnel(ctx, seeded.TenantID, core.PeriodOfDays(time.Now().UTC(), 30))
		require.NoError(t, err)

		assert.False(t, funnel.Potential.IsZero(), "the funnel's top rung must carry the identified waste")
		assert.False(t, funnel.Executed.IsZero(), "one change executed; the executed rung cannot be zero")
		assert.False(t, funnel.Realized.IsZero(), "validation measured a saving; it must reach the realized rung")
		assert.Equal(t, funnel.Realized.Annualized().Micros(), funnel.RealizedAnnual.Micros())
		assert.Greater(t, funnel.Counts[execute.StageRealized], 0)

		// The record for this specific recommendation must have walked the
		// whole ladder rather than being counted by a roll-up that never
		// looked at it.
		record, err := a.Repositories.Savings.Get(ctx, seeded.TenantID, chosen.ID)
		require.NoError(t, err)
		assert.Equal(t, execute.StageRealized, record.Stage)
		assert.False(t, record.Lost)
		assert.Equal(t, plan.ID, record.PlanID)
		assert.GreaterOrEqual(t, len(record.StageHistory), 3,
			"the record must retain how it moved between rungs")
	})

	t.Run("the audit trail tells the whole story", func(t *testing.T) {
		entries, err := svcs.Audit.Timeline(ctx, seeded.TenantID, chosen.ID)
		require.NoError(t, err)
		assert.NotEmpty(t, entries, "an executed change must have an auditable timeline")

		verification, err := svcs.Audit.Verify(ctx, seeded.TenantID,
			time.Now().UTC().Add(-24*time.Hour), time.Now().UTC().Add(time.Hour))
		require.NoError(t, err)
		assert.NotNil(t, verification, "the hash chain must be verifiable")
	})

	t.Run("seeding twice is idempotent", func(t *testing.T) {
		again, err := app.Seed(context.Background(), a)
		require.NoError(t, err)
		assert.True(t, again.AlreadyRan, "a second seed must report the existing tenant, not create another")
		assert.Equal(t, seeded.TenantID, again.TenantID)

		page, err := a.Repositories.Tenants.List(
			core.WithPrincipal(context.Background(), platformAdmin()), ports.ListOptions{Limit: 10})
		require.NoError(t, err)
		assert.Len(t, page.Items, 1, "seeding twice must not create a second tenant")
	})
}

// pickExecutable returns the highest-saving open recommendation the active
// policy permits to run.
//
// It picks by policy outcome rather than by saving alone because the top
// finding on this estate is usually a production change the balanced pack
// prohibits or escalates, and a test that hard-coded one recommendation id
// would break every time a rule's scoring changed.
func pickExecutable(t *testing.T, ctx context.Context, a *app.App, tenant core.TenantID) optimize.Recommendation {
	t.Helper()

	page, err := a.Services.Optimization.List(ctx, tenant,
		ports.RecommendationFilter{Statuses: []optimize.Status{optimize.StatusOpen}},
		ports.ListOptions{Limit: 500})
	require.NoError(t, err)

	var best optimize.Recommendation
	for _, rec := range page.Items {
		if !rec.EstimatedMonthlySaving.GreaterThan(best.EstimatedMonthlySaving) {
			continue
		}
		decision, err := a.Services.Governance.Evaluate(ctx, tenant, rec.ID)
		if err != nil || !decision.Allowed() {
			continue
		}
		if _, ok := a.Repositories.Resources.Get(ctx, tenant, rec.Finding.ResourceID); ok != nil {
			continue
		}
		best = rec
	}
	require.NotEmpty(t, best.ID, "no open recommendation is executable under the active policy")
	return best
}

func platformAdmin() core.Principal {
	p := core.SystemPrincipal("", "e2e")
	p.Roles = append(p.Roles, core.RolePlatformAdmin)
	return p
}

// listOpenRecommendations pages through every open recommendation, because
// the conflict-grouping assertions have to see the whole set: a page-sized
// sample could miss both members of a group and prove nothing.
func listOpenRecommendations(ctx context.Context, a *app.App, tenant core.TenantID) ([]optimize.Recommendation, error) {
	var out []optimize.Recommendation
	opts := ports.ListOptions{Limit: 500}
	for {
		page, err := a.Services.Optimization.List(ctx, tenant,
			ports.RecommendationFilter{Statuses: []optimize.Status{optimize.StatusOpen}}, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.NextCursor == "" || len(page.Items) == 0 {
			return out, nil
		}
		opts.Cursor = page.NextCursor
	}
}
