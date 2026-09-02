package http

import (
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Route is one entry in the platform's declarative route table: an HTTP
// method and pattern, the permission RBAC requires for it, and the handler
// that serves it. Building the whole API surface as data rather than as
// chi.Route(...) calls scattered across per-tag files is what makes two
// things possible: routes_test.go can assert, as a pure data check, that
// every mutating route names a real permission (no route can be forgotten
// silently), and router.go's registration loop is the only place that
// decides how a Route becomes a chi route — one RequirePermission wrap,
// applied identically to all 190-odd entries, rather than a subtly
// different call at each handler.
type Route struct {
	Method     string
	Pattern    string
	Permission core.Permission
	Handler    http.HandlerFunc
	// Name identifies the operation for tests and, eventually, the OpenAPI
	// operationId — kept distinct from Pattern so a path change (rare) does
	// not also break every test that names a route.
	Name string
}

// PublicRoute is a Route with no permission and no principal requirement at
// all — see BuildPublicRoutes below.
type PublicRoute struct {
	Method  string
	Pattern string
	Handler http.HandlerFunc
	Name    string
}

// BuildPublicRoutes returns the Onboarding tag's routes. They are kept out
// of BuildRoutes entirely, not merely given Permission "", because
// routes_test.go's RBAC-exhaustiveness assertion treats every entry in
// BuildRoutes as requiring a real permission on every mutating method — an
// onboarding POST with an intentionally empty permission would either break
// that assertion or force it to special-case onboarding, both worse than
// keeping the two tables apart. router.go mounts these under PublicChain,
// never RequirePermission, and never chi RBAC.
func BuildPublicRoutes(svcs ports.Services) []PublicRoute {
	ob := &onboardingHandler{svc: svcs.Onboarding}
	return []PublicRoute{
		{http.MethodPost, "/onboarding", ob.Start, "onboarding.start"},
		{http.MethodPost, "/onboarding/{conversationID}/messages", ob.Send, "onboarding.send"},
		{http.MethodPost, "/onboarding/{conversationID}/messages/stream", ob.SendStream, "onboarding.send_stream"},
		{http.MethodGet, "/onboarding/{conversationID}", ob.State, "onboarding.state"},
		{http.MethodGet, "/onboarding/{conversationID}/summary", ob.Summarize, "onboarding.summarize"},
		{http.MethodPatch, "/onboarding/{conversationID}", ob.ApplyEdit, "onboarding.apply_edit"},
		{http.MethodPost, "/onboarding/{conversationID}/approve", ob.Approve, "onboarding.approve"},
		{http.MethodPost, "/onboarding/{conversationID}/cancel", ob.Cancel, "onboarding.cancel"},
	}
}

// BuildRoutes returns every authenticated operation in the API, grouped by
// OpenAPI tag in source order (the grouping is purely for readability — chi
// does not care and routes_test.go sorts before comparing).
func BuildRoutes(svcs ports.Services) []Route {
	specs := &specsHandler{svc: svcs.Specs}
	tenants := &tenantsHandler{svc: svcs.Tenants}
	awsAccounts := &awsAccountsHandler{svc: svcs.AWSAccounts}
	discovery := &discoveryHandler{svc: svcs.Discovery}
	twin := &twinHandler{svc: svcs.Twin}
	costs := &costsHandler{svc: svcs.Costs}
	econ := &economicsHandler{svc: svcs.Economics}
	recs := &recommendationsHandler{svc: svcs.Optimization}
	sims := &simulationsHandler{svc: svcs.Simulation}
	gov := &governanceHandler{svc: svcs.Governance}
	auto := &automationHandler{svc: svcs.Automation}
	copilot := &copilotHandler{svc: svcs.Copilot}
	audit := &auditHandler{svc: svcs.Audit}
	authH := authHandler{}

	return []Route{
		// --- Authentication ---------------------------------------------
		{http.MethodGet, "/auth/whoami", "", authH.WhoAmI, "auth.whoami"},

		// --- Specs --------------------------------------------------------
		{http.MethodGet, "/specs/active", core.PermSpecRead, specs.GetActive, "specs.get_active"},
		{http.MethodGet, "/specs/diff", core.PermSpecRead, specs.Diff, "specs.diff"},
		{http.MethodGet, "/specs", core.PermSpecRead, specs.ListVersions, "specs.list_versions"},
		{http.MethodPost, "/specs/validate", core.PermSpecRead, specs.Validate, "specs.validate"},
		{http.MethodPost, "/specs/revisions", core.PermSpecWrite, specs.ProposeRevision, "specs.propose_revision"},
		{http.MethodPost, "/specs/import", core.PermSpecWrite, specs.ImportYAML, "specs.import_yaml"},
		{http.MethodGet, "/specs/{id}", core.PermSpecRead, specs.Get, "specs.get"},
		{http.MethodGet, "/specs/{id}/export", core.PermSpecRead, specs.ExportYAML, "specs.export_yaml"},
		{http.MethodPost, "/specs/{id}/approve", core.PermSpecApprove, specs.Approve, "specs.approve"},
		{http.MethodPost, "/specs/{id}/reject", core.PermSpecApprove, specs.Reject, "specs.reject"},

		// --- Tenants --------------------------------------------------------
		{http.MethodGet, "/tenant", core.PermTenantAdmin, tenants.Get, "tenants.get"},
		{http.MethodPatch, "/tenant", core.PermTenantAdmin, tenants.Update, "tenants.update"},

		// --- Users --------------------------------------------------------
		{http.MethodGet, "/tenant/users", core.PermTenantAdmin, tenants.ListUsers, "users.list"},
		{http.MethodPost, "/tenant/users", core.PermTenantAdmin, tenants.InviteUser, "users.invite"},
		{http.MethodPatch, "/tenant/users/{userID}/roles", core.PermTenantAdmin, tenants.UpdateRoles, "users.update_roles"},
		{http.MethodDelete, "/tenant/users/{userID}", core.PermTenantAdmin, tenants.RemoveUser, "users.remove"},

		// --- AWS Accounts ---------------------------------------------------
		{http.MethodPost, "/aws-accounts", core.PermAWSConnect, awsAccounts.Register, "aws_accounts.register"},
		{http.MethodGet, "/aws-accounts", core.PermResourceRead, awsAccounts.List, "aws_accounts.list"},
		{http.MethodGet, "/aws-accounts/{id}", core.PermResourceRead, awsAccounts.Get, "aws_accounts.get"},
		{http.MethodPost, "/aws-accounts/{id}/verify", core.PermAWSConnect, awsAccounts.Verify, "aws_accounts.verify"},
		{http.MethodPost, "/aws-accounts/{id}/suspend", core.PermAWSConnect, awsAccounts.Suspend, "aws_accounts.suspend"},
		{http.MethodDelete, "/aws-accounts/{id}", core.PermAWSConnect, awsAccounts.Remove, "aws_accounts.remove"},
		{http.MethodGet, "/aws-accounts/{id}/instructions", core.PermAWSConnect, awsAccounts.Instructions, "aws_accounts.instructions"},

		// --- Discovery ------------------------------------------------------
		{http.MethodPost, "/discovery/runs", core.PermDiscoveryRun, discovery.Run, "discovery.run"},
		{http.MethodGet, "/discovery/runs", core.PermResourceRead, discovery.ListRuns, "discovery.list_runs"},
		{http.MethodGet, "/discovery/status", core.PermResourceRead, discovery.Status, "discovery.status"},
		{http.MethodGet, "/discovery/runs/{runID}", core.PermResourceRead, discovery.Get, "discovery.get"},
		{http.MethodGet, "/discovery/runs/{runID}/stream", core.PermResourceRead, discovery.GetStream, "discovery.get_stream"},

		// --- Architecture (twin) --------------------------------------------
		{http.MethodGet, "/architecture/graph", core.PermResourceRead, twin.Graph, "architecture.graph"},
		{http.MethodGet, "/architecture/cost-flow", core.PermCostRead, twin.CostFlow, "architecture.cost_flow"},
		{http.MethodPost, "/architecture/rebuild", core.PermDiscoveryRun, twin.Rebuild, "architecture.rebuild"},
		{http.MethodGet, "/architecture/nodes/{id}", core.PermResourceRead, twin.Node, "architecture.node"},
		{http.MethodGet, "/architecture/nodes/{id}/dependents", core.PermResourceRead, twin.Dependents, "architecture.dependents"},

		// --- Resources --------------------------------------------------------
		{http.MethodGet, "/resources", core.PermResourceRead, twin.ListResources, "resources.list"},
		{http.MethodGet, "/resources/{id}", core.PermResourceRead, twin.GetResource, "resources.get"},

		// --- Costs --------------------------------------------------------
		{http.MethodPost, "/costs/ingest", core.PermCostRead, costs.Ingest, "costs.ingest"},
		{http.MethodGet, "/costs/summary", core.PermCostRead, costs.Summary, "costs.summary"},
		{http.MethodGet, "/costs/series", core.PermCostRead, costs.Series, "costs.series"},
		{http.MethodGet, "/costs/breakdown", core.PermCostRead, costs.Breakdown, "costs.breakdown"},
		{http.MethodGet, "/costs/forecast", core.PermCostRead, costs.Forecast, "costs.forecast"},
		{http.MethodGet, "/costs/explain", core.PermCostRead, costs.Explain, "costs.explain"},
		{http.MethodPost, "/costs/anomalies/detect", core.PermCostRead, costs.DetectAnomalies, "costs.detect_anomalies"},
		{http.MethodGet, "/costs/anomalies", core.PermCostRead, costs.ListAnomalies, "costs.list_anomalies"},

		// --- Economics --------------------------------------------------------
		{http.MethodPost, "/economics/compute", core.PermEconomicsRead, econ.Compute, "economics.compute"},
		{http.MethodGet, "/economics/footprints", core.PermEconomicsRead, econ.ListFootprints, "economics.list_footprints"},
		{http.MethodGet, "/economics/footprints/{id}", core.PermEconomicsRead, econ.Footprint, "economics.footprint"},
		{http.MethodPost, "/economics/transactions", economicsWritePermission, econ.UpsertTransaction, "economics.upsert_transaction"},
		{http.MethodGet, "/economics/transactions", core.PermEconomicsRead, econ.ListTransactions, "economics.list_transactions"},
		{http.MethodGet, "/economics/transactions/{id}/unit-economics", core.PermEconomicsRead, econ.UnitEconomics, "economics.unit_economics"},
		{http.MethodGet, "/economics/transactions/{id}/unit-economics/history", core.PermEconomicsRead, econ.UnitEconomicsHistory, "economics.unit_economics_history"},
		{http.MethodGet, "/economics/efficiency-score", core.PermEconomicsRead, econ.EfficiencyScore, "economics.efficiency_score"},
		{http.MethodGet, "/economics/executive-summary", core.PermEconomicsRead, econ.ExecutiveSummary, "economics.executive_summary"},

		// --- Cost SLOs --------------------------------------------------------
		{http.MethodPost, "/cost-slos", core.PermSLOWrite, econ.UpsertCostSLO, "cost_slos.upsert"},
		{http.MethodGet, "/cost-slos", core.PermEconomicsRead, econ.ListCostSLOs, "cost_slos.list"},
		{http.MethodPost, "/cost-slos/evaluate", core.PermEconomicsRead, econ.EvaluateSLOs, "cost_slos.evaluate"},
		{http.MethodGet, "/cost-slos/budget-states", core.PermEconomicsRead, econ.BudgetStates, "cost_slos.budget_states"},
		{http.MethodDelete, "/cost-slos/{id}", core.PermSLOWrite, econ.DeleteCostSLO, "cost_slos.delete"},

		// --- Recommendations --------------------------------------------------
		{http.MethodPost, "/recommendations/analyze", core.PermRecommendRun, recs.Analyze, "recommendations.analyze"},
		{http.MethodGet, "/recommendations", core.PermRecommendRead, recs.List, "recommendations.list"},
		{http.MethodGet, "/recommendations/summary", core.PermRecommendRead, recs.Summary, "recommendations.summary"},
		{http.MethodGet, "/recommendations/rules", core.PermRecommendRead, recs.ListRules, "recommendations.list_rules"},
		{http.MethodGet, "/recommendations/{id}", core.PermRecommendRead, recs.Get, "recommendations.get"},
		{http.MethodGet, "/recommendations/{id}/explain", core.PermRecommendRead, recs.Explain, "recommendations.explain"},
		{http.MethodPost, "/recommendations/{id}/dismiss", core.PermRecommendRun, recs.Dismiss, "recommendations.dismiss"},
		{http.MethodPost, "/recommendations/{id}/snooze", core.PermRecommendRun, recs.Snooze, "recommendations.snooze"},
		{http.MethodPost, "/recommendations/{recommendationID}/execution-plan", core.PermExecutionStart, auto.PlanExecution, "recommendations.plan_execution"},
		{http.MethodGet, "/recommendations/{recommendationID}/policy-decision", core.PermPolicyRead, gov.Evaluate, "recommendations.policy_decision"},

		// --- Simulations --------------------------------------------------
		{http.MethodPost, "/simulations/mutate", core.PermSimulationRun, sims.Mutate, "simulations.mutate"},
		{http.MethodPost, "/simulations/counterfactual", core.PermSimulationRun, sims.Counterfactual, "simulations.counterfactual"},
		{http.MethodGet, "/simulations", core.PermSimulationRun, sims.ListSimulations, "simulations.list"},
		{http.MethodGet, "/simulations/{id}", core.PermSimulationRun, sims.GetSimulation, "simulations.get"},

		// --- Cost Compiler --------------------------------------------------
		{http.MethodPost, "/compiler/compile", core.PermCompilerRun, sims.Compile, "compiler.compile"},
		{http.MethodGet, "/compiler/compilations/{id}", core.PermCompilerRun, sims.GetCompilation, "compiler.get_compilation"},

		// --- Cost Regression --------------------------------------------------
		{http.MethodPost, "/compiler/compilations/{compilationID}/regression", core.PermCompilerRun, sims.RunRegression, "regression.run"},
		{http.MethodGet, "/regression/suites", core.PermCompilerRun, sims.ListRegressionSuites, "regression.list_suites"},
		{http.MethodPost, "/regression/suites", core.PermCompilerRun, sims.UpsertRegressionSuite, "regression.upsert_suite"},

		// --- Policies --------------------------------------------------------
		{http.MethodGet, "/policies/active", core.PermPolicyRead, gov.GetPolicy, "policies.get_active"},
		{http.MethodGet, "/policies/versions", core.PermPolicyRead, gov.ListPolicyVersions, "policies.list_versions"},
		{http.MethodPut, "/policies", core.PermPolicyWrite, gov.SavePolicy, "policies.save"},
		{http.MethodPost, "/policies/validate", core.PermPolicyRead, gov.ValidatePolicy, "policies.validate"},
		{http.MethodPost, "/policies/simulate", core.PermPolicyRead, gov.Simulate, "policies.simulate"},
		{http.MethodPost, "/policies/{id}/activate", core.PermPolicyWrite, gov.ActivatePolicy, "policies.activate"},

		// --- Approvals --------------------------------------------------------
		{http.MethodGet, "/approvals", core.PermApprovalRead, gov.ListApprovals, "approvals.list"},
		{http.MethodPost, "/approvals", core.PermApprovalRead, gov.RequestApproval, "approvals.request"},
		{http.MethodGet, "/approvals/{id}", core.PermApprovalRead, gov.GetApproval, "approvals.get"},
		{http.MethodPost, "/approvals/{id}/decide", core.PermApprovalDecide, gov.Decide, "approvals.decide"},

		// --- Automation --------------------------------------------------------
		{http.MethodPost, "/automation/process", core.PermAutomationWrite, auto.ProcessAutonomous, "automation.process"},
		{http.MethodPost, "/automation/learn", core.PermAutomationWrite, auto.Learn, "automation.learn"},

		// --- Executions --------------------------------------------------------
		{http.MethodGet, "/executions", core.PermExecutionRead, auto.ListPlans, "executions.list"},
		{http.MethodGet, "/executions/{id}", core.PermExecutionRead, auto.GetPlan, "executions.get"},
		{http.MethodGet, "/executions/{id}/stream", core.PermExecutionRead, auto.ExecutionStream, "executions.stream"},
		{http.MethodPost, "/executions/{id}/execute", core.PermExecutionStart, auto.Execute, "executions.execute"},
		{http.MethodPost, "/executions/{id}/cancel", core.PermExecutionCancel, auto.Cancel, "executions.cancel"},
		{http.MethodPost, "/executions/{id}/validate", core.PermExecutionRead, auto.Validate, "executions.validate"},
		{http.MethodPost, "/executions/{id}/rollback", core.PermRollbackStart, auto.Rollback, "executions.rollback"},

		// --- Savings --------------------------------------------------------
		{http.MethodGet, "/savings/funnel", core.PermExecutionRead, auto.Funnel, "savings.funnel"},

		// --- Audit --------------------------------------------------------
		{http.MethodGet, "/audit", core.PermAuditRead, audit.Query, "audit.query"},
		{http.MethodGet, "/audit/verify", core.PermAuditRead, audit.Verify, "audit.verify"},
		{http.MethodGet, "/audit/recommendations/{recommendationID}/timeline", core.PermAuditRead, audit.Timeline, "audit.timeline"},

		// --- AI Copilot --------------------------------------------------------
		{http.MethodPost, "/copilot/ask", core.PermCopilotUse, copilot.Ask, "copilot.ask"},
		{http.MethodPost, "/copilot/ask/stream", core.PermCopilotUse, copilot.AskStream, "copilot.ask_stream"},
		{http.MethodGet, "/copilot/conversations", core.PermCopilotUse, copilot.ListConversations, "copilot.list_conversations"},
		{http.MethodGet, "/copilot/conversations/{id}", core.PermCopilotUse, copilot.GetConversation, "copilot.get_conversation"},
		{http.MethodGet, "/copilot/suggestions", core.PermCopilotUse, copilot.Suggestions, "copilot.suggestions"},
	}
}

// isMutating (idempotency.go) reports whether method changes state — used by
// auditMiddleware (middleware.go) and by routes_test.go's
// RBAC-exhaustiveness assertion, which requires every mutating Route to name
// a non-empty Permission (GET is exempt: a handful of read routes, like
// whoami, legitimately need nothing beyond authentication).
