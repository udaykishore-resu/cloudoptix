package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/app"
	"github.com/udaykishore-resu/cloudoptix/internal/application/copilot"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// devToken is the fixed local-development bearer token the harness's config
// enables. It resolves to a principal scoped to app.DemoTenantID and nothing
// else, which is exactly what makes it useful here: the HTTP assertions
// below present a real, valid credential for tenant A and ask for tenant B's
// data with it.
const devToken = "isolation-test-token"

// TestTenantIsolation is a hostile test. Two tenants are populated with real
// data from the same underlying estate — so their resources share native ids,
// names and shapes, which is the condition under which a forgotten tenant
// filter is most likely to return the wrong rows — and then every read path
// is attempted across the boundary.
//
// A pass means: nothing returns another tenant's data, and every refusal is
// either a not-found or an explicit tenant mismatch. A 500, a panic, or an
// empty-but-successful response would all be failures — the last one most of
// all, because "no results" and "not yours" are different answers and
// conflating them is how a leak stays invisible until it is a large one.
//
// Traceability: REQ-SEC-003, SPEC-SEC-003.
func TestTenantIsolation(t *testing.T) {
	a := newApp(t)

	// Tenant A is the demo tenant, seeded through the real onboarding path.
	seeded, err := app.Seed(context.Background(), a)
	require.NoError(t, err)
	tenantA := seeded.TenantID
	ctxA := adminCtx(tenantA)

	// Tenant B is a second, independently onboarded tenant against the same
	// simulated estate.
	tenantB := seedSecondTenant(t, a)
	ctxB := adminCtx(tenantB)

	require.NotEqual(t, tenantA, tenantB)

	fixtureA := loadFixture(t, ctxA, a, tenantA)
	fixtureB := loadFixture(t, ctxB, a, tenantB)

	t.Run("both tenants really do hold data", func(t *testing.T) {
		// Without this, every isolation assertion below could pass
		// vacuously against an empty tenant.
		assert.NotEmpty(t, fixtureA.resourceID)
		assert.NotEmpty(t, fixtureB.resourceID)
		assert.NotEmpty(t, fixtureA.recommendationID)
		assert.NotEmpty(t, fixtureB.recommendationID)
		assert.NotEqual(t, fixtureA.resourceID, fixtureB.resourceID,
			"the two tenants must hold distinct rows even though they describe the same estate")
	})

	t.Run("services refuse cross-tenant reads", func(t *testing.T) {
		svcs := a.Services

		// Each case names a read path and attempts it as tenant A against
		// tenant B's identifier. The table exists so adding a service to
		// ports.Services and forgetting to guard it shows up as a missing
		// row here rather than as nothing at all.
		cases := []struct {
			name string
			call func() error
		}{
			{"tenants.get", func() error {
				_, err := svcs.Tenants.Get(ctxA, tenantB)
				return err
			}},
			{"specs.get_active", func() error {
				_, err := svcs.Specs.GetActive(ctxA, tenantB)
				return err
			}},
			{"aws_accounts.get", func() error {
				_, err := svcs.AWSAccounts.Get(ctxA, tenantB, fixtureB.accountID)
				return err
			}},
			{"discovery.status", func() error {
				_, err := svcs.Discovery.Status(ctxA, tenantB)
				return err
			}},
			{"twin.node", func() error {
				_, err := svcs.Twin.Node(ctxA, tenantB, fixtureB.resourceID)
				return err
			}},
			{"twin.graph", func() error {
				_, err := svcs.Twin.Graph(ctxA, tenantB, ports.TwinQuery{View: "architecture"})
				return err
			}},
			{"costs.summary", func() error {
				_, err := svcs.Costs.Summary(ctxA, tenantB, core.PeriodOfDays(time.Now().UTC(), 30))
				return err
			}},
			{"economics.list_transactions", func() error {
				_, err := svcs.Economics.ListTransactions(ctxA, tenantB)
				return err
			}},
			{"economics.executive_summary", func() error {
				_, err := svcs.Economics.ExecutiveSummary(ctxA, tenantB)
				return err
			}},
			{"optimization.get", func() error {
				_, err := svcs.Optimization.Get(ctxA, tenantB, fixtureB.recommendationID)
				return err
			}},
			{"optimization.explain", func() error {
				_, err := svcs.Optimization.Explain(ctxA, tenantB, fixtureB.recommendationID)
				return err
			}},
			{"optimization.summary", func() error {
				_, err := svcs.Optimization.Summary(ctxA, tenantB)
				return err
			}},
			{"governance.get_policy", func() error {
				_, err := svcs.Governance.GetPolicy(ctxA, tenantB)
				return err
			}},
			{"governance.evaluate", func() error {
				_, err := svcs.Governance.Evaluate(ctxA, tenantB, fixtureB.recommendationID)
				return err
			}},
			{"automation.list_plans", func() error {
				_, err := svcs.Automation.ListPlans(ctxA, tenantB, nil, ports.ListOptions{})
				return err
			}},
			{"automation.funnel", func() error {
				_, err := svcs.Automation.Funnel(ctxA, tenantB, core.PeriodOfDays(time.Now().UTC(), 30))
				return err
			}},
			{"audit.query", func() error {
				_, err := svcs.Audit.Query(ctxA, tenantB, ports.AuditQuery{Limit: 10})
				return err
			}},
			{"copilot.ask", func() error {
				_, err := svcs.Copilot.Ask(ctxA, tenantB, ports.CopilotRequest{
					Question: "What is our monthly spend?", Actor: "attacker",
				})
				return err
			}},
			{"copilot.suggestions", func() error {
				_, err := svcs.Copilot.Suggestions(ctxA, tenantB)
				return err
			}},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				err := tc.call()
				requireIsolationError(t, err)
			})
		}
	})

	t.Run("repositories refuse cross-tenant reads", func(t *testing.T) {
		repos := a.Repositories

		cases := []struct {
			name string
			call func() error
		}{
			{"tenants.get", func() error { _, err := repos.Tenants.Get(ctxA, tenantB); return err }},
			{"tenants.get_by_slug", func() error {
				_, err := repos.Tenants.GetBySlug(ctxA, secondTenantSlug)
				return err
			}},
			{"specs.get_active", func() error { _, err := repos.Specs.GetActive(ctxA, tenantB); return err }},
			{"aws_accounts.list", func() error { _, err := repos.AWSAccounts.List(ctxA, tenantB); return err }},
			{"resources.get", func() error {
				_, err := repos.Resources.Get(ctxA, tenantB, fixtureB.resourceID)
				return err
			}},
			{"resources.load_inventory", func() error {
				_, err := repos.Resources.LoadInventory(ctxA, tenantB, ports.ResourceFilter{})
				return err
			}},
			{"resources.count", func() error {
				_, err := repos.Resources.Count(ctxA, tenantB, ports.ResourceFilter{})
				return err
			}},
			{"costs.total", func() error {
				_, err := repos.Costs.Total(ctxA, tenantB, ports.CostFilter{
					Period: core.PeriodOfDays(time.Now().UTC(), 30),
				})
				return err
			}},
			{"metrics.get_summary", func() error {
				_, err := repos.Metrics.GetSummary(ctxA, tenantB, fixtureB.resourceID)
				return err
			}},
			{"recommendations.get", func() error {
				_, err := repos.Recommendations.Get(ctxA, tenantB, fixtureB.recommendationID)
				return err
			}},
			{"recommendations.list", func() error {
				_, err := repos.Recommendations.List(ctxA, tenantB, ports.RecommendationFilter{}, ports.ListOptions{})
				return err
			}},
			{"policies.get_active", func() error { _, err := repos.Policies.GetActive(ctxA, tenantB); return err }},
			{"economics.list_transactions", func() error {
				_, err := repos.Economics.ListTransactions(ctxA, tenantB)
				return err
			}},
			{"savings.funnel", func() error {
				_, err := repos.Savings.Funnel(ctxA, tenantB, core.PeriodOfDays(time.Now().UTC(), 30))
				return err
			}},
			{"discovery_runs.list_recent", func() error {
				_, err := repos.DiscoveryRuns.ListRecent(ctxA, tenantB, 5)
				return err
			}},
			{"applications.list", func() error {
				_, err := repos.Applications.ListApplications(ctxA, tenantB)
				return err
			}},
			{"conversations.list", func() error {
				_, err := repos.Conversations.List(ctxA, tenantB, ports.ConversationCopilot, ports.ListOptions{})
				return err
			}},
			{"audit.query", func() error {
				_, err := repos.Audit.Query(ctxA, audit.Query{TenantID: tenantB, Limit: 10})
				return err
			}},
			{"audit.head", func() error {
				_, _, err := repos.Audit.Head(ctxA, tenantB)
				return err
			}},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				requireIsolationError(t, tc.call())
			})
		}
	})

	t.Run("the cache is partitioned by tenant", func(t *testing.T) {
		const key = "isolation-probe"
		require.NoError(t, a.Cache.Set(ctxA, tenantA, key, map[string]string{"secret": "tenant-a"}, time.Minute))

		// The key is identical and the caller is legitimate for its own
		// tenant; only the partition stops the read. This is the case a
		// caller-supplied prefix scheme gets wrong.
		var got map[string]string
		found, err := a.Cache.Get(ctxB, tenantB, key, &got)
		require.NoError(t, err)
		assert.False(t, found, "tenant B read tenant A's cache entry: %v", got)

		found, err = a.Cache.Get(ctxA, tenantA, key, &got)
		require.NoError(t, err)
		require.True(t, found, "the entry must still be readable by the tenant that wrote it")
		assert.Equal(t, "tenant-a", got["secret"])

		require.NoError(t, a.Cache.InvalidatePrefix(ctxB, tenantB, "isolation"))
		found, err = a.Cache.Get(ctxA, tenantA, key, &got)
		require.NoError(t, err)
		assert.True(t, found, "tenant B's invalidation must not clear tenant A's entries")
	})

	t.Run("the API refuses a cross-tenant identifier", func(t *testing.T) {
		// The dev token is bound to tenant A. These requests are perfectly
		// authenticated and perfectly authorized — they simply ask for rows
		// belonging to someone else.
		for _, path := range []string{
			"/api/v1/resources/" + string(fixtureB.resourceID),
			"/api/v1/recommendations/" + string(fixtureB.recommendationID),
			"/api/v1/recommendations/" + string(fixtureB.recommendationID) + "/explain",
			"/api/v1/aws-accounts/" + string(fixtureB.accountID),
		} {
			t.Run(path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer "+devToken)
				a.Router.ServeHTTP(rec, req)

				assert.Contains(t, []int{http.StatusNotFound, http.StatusForbidden}, rec.Code,
					"expected 404 or 403 for a cross-tenant identifier, got %d: %s", rec.Code, rec.Body.String())
				assert.NotContains(t, rec.Body.String(), string(tenantB),
					"the response body names the other tenant")
			})
		}
	})

	t.Run("the API's list endpoints return only the caller's rows", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations?limit=200", nil)
		req.Header.Set("Authorization", "Bearer "+devToken)
		a.Router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var page struct {
			Items []struct {
				ID       core.ID       `json:"id"`
				TenantID core.TenantID `json:"tenant_id"`
			} `json:"items"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		require.NotEmpty(t, page.Items, "the list must actually return rows for the assertion to mean anything")
		for _, item := range page.Items {
			assert.Equal(t, tenantA, item.TenantID, "recommendation %s belongs to %s", item.ID, item.TenantID)
		}
	})
}

// tenantFixture is one tenant's set of identifiers, used as the "ask for
// this" side of every cross-tenant attempt.
type tenantFixture struct {
	accountID        core.ID
	resourceID       core.ID
	recommendationID core.ID
	nativeID         string
}

func loadFixture(t *testing.T, ctx context.Context, a *app.App, tenant core.TenantID) tenantFixture {
	t.Helper()
	var f tenantFixture

	accounts, err := a.Repositories.AWSAccounts.List(ctx, tenant)
	require.NoError(t, err)
	require.NotEmpty(t, accounts)
	f.accountID = accounts[0].ID

	resources, err := a.Repositories.Resources.List(ctx, tenant,
		ports.ResourceFilter{Kinds: []cloud.Kind{cloud.KindEC2Instance}}, ports.ListOptions{Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, resources.Items)
	f.resourceID = resources.Items[0].ID
	f.nativeID = resources.Items[0].NativeID

	recs, err := a.Repositories.Recommendations.List(ctx, tenant,
		ports.RecommendationFilter{Statuses: []optimize.Status{optimize.StatusOpen}},
		ports.ListOptions{Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, recs.Items)
	f.recommendationID = recs.Items[0].ID

	return f
}

// requireIsolationError asserts that err is a refusal of the right shape.
//
// Both ErrNotFound and ErrTenantMismatch are accepted, and the distinction
// is deliberate rather than sloppy: a repository that scopes its query by
// tenant genuinely cannot find the row and says so, while one that finds it
// and then checks says mismatch. Both are correct refusals. What is not
// accepted is a nil error — that is a leak — or an error of any other class,
// which would mean the boundary was crossed and something else failed
// afterwards.
func requireIsolationError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err, "a cross-tenant read returned no error at all")
	if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrTenantMismatch) ||
		errors.Is(err, core.ErrForbidden) || errors.Is(err, core.ErrUnauthenticated) {
		return
	}
	t.Fatalf("cross-tenant read failed with an unexpected error class: %v", err)
}

// assertNoLeak fails if a cross-tenant tool call disclosed anything about
// the other tenant.
//
// A copilot tool reports a refusal as a payload rather than a Go error (the
// agentic loop feeds tool failures back to the model as data), so a
// {"error": ...} payload is a correct refusal, not a leak — even though it
// echoes the identifier the caller supplied. Echoing the caller's own
// argument back discloses nothing; the caller already had it. What would be
// a leak is that payload carrying anything else, so the refusal branch
// asserts the payload holds only the error and the error names a genuine
// isolation refusal.
func assertNoLeak(t *testing.T, payload any, other tenantFixture) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err == nil {
		if msg, refused := fields["error"].(string); refused {
			assert.Len(t, fields, 1,
				"a refusal payload must carry nothing but the error, got %s", string(encoded))
			assert.True(t,
				containsAny(msg, "tenant_mismatch", "not found", "could not find", "forbidden"),
				"refusal does not name an isolation failure: %s", msg)
			return
		}
	}

	body := string(encoded)
	for label, needle := range map[string]string{
		"resource id":       string(other.resourceID),
		"recommendation id": string(other.recommendationID),
		"account id":        string(other.accountID),
	} {
		if needle == "" {
			continue
		}
		assert.NotContains(t, body, needle, "payload leaked the other tenant's %s", label)
	}
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

const (
	secondTenantSlug = "northwind-demo"
	secondTenantName = "Northwind"
	secondTenantID   = core.TenantID("northwind-demo")

	thirdTenantSlug = "eastwind-demo"
	thirdTenantName = "Eastwind"
	thirdTenantID   = core.TenantID("eastwind-demo")
)

// seedSecondTenant onboards the isolation test's tenant B against the same
// simulated estate, through the same conversational path tenant A used.
func seedSecondTenant(t *testing.T, a *app.App) core.TenantID {
	t.Helper()
	return seedLightTenant(t, a, secondTenantID, secondTenantSlug, secondTenantName)
}

// seedLightTenant onboards one tenant against the shared simulated estate:
// a real conversation, a verified simulated account, a discovery run, a week
// of cost history and an optimization pass.
//
// Sharing the estate is the point. Two tenants seeded this way hold rows
// describing the same underlying AWS resources, with the same native ids and
// the same names. A tenant filter that is present but wrong — matching on
// native id, say, or on a cache key a caller supplied — returns the wrong
// rows in exactly this configuration and in almost no other.
func seedLightTenant(t *testing.T, a *app.App, id core.TenantID, slug, name string) core.TenantID {
	t.Helper()
	ctx := context.Background()

	turns := []string{
		"We are " + name + ", a logistics company. We're a mid-size company operating across North America.",
		"Our platform is called Freightline, a shipment tracking system built as microservices on ECS and Lambda. " +
			"We use PostgreSQL and DynamoDB.",
		"Please analyse AWS account 412984773301 in us-east-1, that's our production environment.",
		"We handle about 300,000 shipments per month and need to meet SOC2.",
		"We want to cut costs by 15% and our monthly budget is $200,000. " +
			"Our availability target is 99.9% and max latency should stay under 400ms.",
		"We have a low risk tolerance for optimization changes, and production changes always require human approval.",
	}

	actor := "admin@" + slug + ".example"
	state, err := a.Services.Onboarding.Start(ctx, ports.StartOnboardingInput{
		Actor: actor, ActorEmail: actor,
		InitialMessage: turns[0], ExistingTenant: id,
	})
	require.NoError(t, err)
	for _, msg := range turns[1:] {
		state, err = a.Services.Onboarding.Send(ctx, state.ConversationID, msg)
		require.NoError(t, err)
	}

	summary, err := a.Services.Onboarding.Summarize(ctx, state.ConversationID)
	require.NoError(t, err)
	require.True(t, summary.CanApprove, "blocking: %v", summary.BlockingReasons)

	result, err := a.Services.Onboarding.Approve(ctx, ports.ApproveOnboardingInput{
		ConversationID: state.ConversationID,
		Actor:          actor, ActorEmail: actor,
		TenantName: name, TenantSlug: slug,
		Plan: tenancy.PlanStandard, Demo: true,
	})
	require.NoError(t, err)
	tenant := result.Tenant.ID
	ctxT := adminCtx(tenant)

	account, _, err := a.Services.AWSAccounts.Register(ctxT, tenant, ports.RegisterAccountInput{
		AccountID: a.Estate.AccountID, Alias: a.Estate.Alias,
		Environment: core.EnvProduction, Regions: a.Estate.Regions,
		AccessMode: cloud.AccessSimulated, IsPayer: true,
	})
	require.NoError(t, err)
	_, _, err = a.Services.AWSAccounts.Verify(ctxT, tenant, account.ID)
	require.NoError(t, err)

	run, err := a.Services.Discovery.Run(ctxT, tenant, ports.DiscoveryRequest{
		AccountID: account.ID, Trigger: "onboarding", IncludeMetrics: true,
	})
	require.NoError(t, err)
	require.Greater(t, run.ResourcesDiscovered, 0)

	// A week rather than the demo tenant's ninety days. Tenant B exists to
	// be attacked, not analysed, and memstore's UnitOfWork deep-copies the
	// whole store per transaction (see memstore.Store.Do) — so every cost
	// row here is paid for again on every one of the sixteen copilot tool
	// invocations below. Seven days is enough for the cross-tenant cost
	// reads to be attempted against real rows.
	_, err = a.Services.Costs.Ingest(ctxT, tenant, account.ID, core.PeriodOfDays(time.Now().UTC(), 7))
	require.NoError(t, err)

	_, err = a.Services.Economics.UpsertTransaction(ctxT, econ.BusinessTransaction{
		TenantID: tenant, Name: "shipment",
		WorkloadIDs:  []core.ID{core.NewID("wl")},
		VolumeSource: econ.VolumeSource{Kind: "declared", DeclaredMonthly: 300_000},
	})
	require.NoError(t, err)

	analyzed, err := a.Services.Optimization.Analyze(ctxT, tenant, ports.AnalyzeRequest{})
	require.NoError(t, err)
	require.Greater(t, analyzed.RecommendationsCreated, 0)

	return tenant
}

// TestCopilotToolsAreTenantScoped invokes every tool the copilot can offer a
// model as one tenant against another tenant's identifiers.
//
// The tools are the widest read surface a model can reach: each takes an
// explicit tenant argument and opens its own read transaction, so a tool
// that trusted its argument rather than the caller's scope would be a leak
// the agentic loop could be talked into triggering.
//
// It runs on its own application rather than sharing TestTenantIsolation's,
// with two lightly-seeded tenants instead of the full demo seed. That is a
// runtime decision, not a coverage one: memstore's UnitOfWork deep-copies
// the whole store per transaction (see memstore.Store.Do's own doc comment),
// so every one of these sixteen invocations pays for a copy of everything in
// the store — and the demo seed's ninety days of cost history for 870
// resources dominates that copy while proving nothing about tool scoping.
// Two tenants with real resources, metrics and recommendations is exactly
// what the invariant needs.
func TestCopilotToolsAreTenantScoped(t *testing.T) {
	a := newApp(t)

	tenantA := seedLightTenant(t, a, secondTenantID, secondTenantSlug, secondTenantName)
	tenantB := seedLightTenant(t, a, thirdTenantID, thirdTenantSlug, thirdTenantName)
	require.NotEqual(t, tenantA, tenantB)

	ctxA := adminCtx(tenantA)
	fixtureB := loadFixture(t, adminCtx(tenantB), a, tenantB)

	// The registry is rebuilt from the same two dependencies copilot.New
	// passes to BuildRegistry, so this is the exact tool set the running
	// service offers the model — reconstructing it is what lets each tool be
	// invoked directly rather than coaxing the agentic loop into calling all
	// sixteen.
	registry := copilot.BuildRegistry(a.UnitOfWork, a.Knowledge)
	require.NotEmpty(t, registry.Names())

	for _, name := range registry.Names() {
		tool, ok := registry.Get(name)
		require.True(t, ok)

		t.Run(name, func(t *testing.T) {
			result, err := tool.Invoke(ctxA, tenantB, map[string]any{
				"id":                string(fixtureB.resourceID),
				"resource_id":       string(fixtureB.resourceID),
				"recommendation_id": string(fixtureB.recommendationID),
				"query":             "monthly spend",
				"limit":             5,
			})
			if err != nil {
				requireIsolationError(t, err)
				return
			}
			// A tool that returns without error must not have returned
			// tenant B's data. Serialising and searching for B's identifiers
			// is blunt on purpose: it catches a leak regardless of which
			// field of which nested struct carried it.
			assertNoLeak(t, result, fixtureB)
		})
	}
}
