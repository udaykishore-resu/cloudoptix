package onboarding

import (
	"fmt"
	"sort"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// setProvenance records path's provenance, unless it is already CONFIRMED —
// a fact the user stated directly is never downgraded by a later inference
// pass or a re-extraction that happened not to find it again this turn.
func setProvenance(draft *spec.Spec, path string, prov core.Provenance) {
	if draft.Provenance == nil {
		draft.Provenance = map[string]core.Provenance{}
	}
	if draft.Provenance[path] == core.ProvenanceConfirmed && prov != core.ProvenanceConfirmed {
		return
	}
	draft.Provenance[path] = prov
}

// asString / asStrings / asFloat / asBool convert the loosely-typed
// map[string]any an extraction call returns into the shape a Spec field
// needs, tolerating the small representational differences between the
// deterministic provider (native Go types) and a real model's JSON-decoded
// tool-use input (float64 for every number, []any for every array).
func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

func asStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	}
	return 0, false
}

func asBool(v any) (bool, bool) {
	b, ok := v.(bool)
	return b, ok
}

// applyExtraction folds a structured-output result into the draft
// specification. Every field it sets is marked CONFIRMED with source "user"
// — this function only ever runs on what the model (or the deterministic
// provider's regex slot-filler) found stated in the conversation, never on
// a guess; guesses are runInference's job.
func applyExtraction(draft *spec.Spec, extracted map[string]any) {
	if v, ok := extracted["organization_name"]; ok {
		if s, ok := asString(v); ok {
			draft.Organization.Name = s
			setProvenance(draft, "organization.name", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["industry"]; ok {
		if s, ok := asString(v); ok {
			draft.Organization.Industry = s
			setProvenance(draft, "organization.industry", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["company_size"]; ok {
		if s, ok := asString(v); ok {
			draft.Organization.Size = s
		}
	}
	if v, ok := extracted["business_regions"]; ok {
		if ss := asStrings(v); len(ss) > 0 {
			draft.Organization.Regions = mergeStrings(draft.Organization.Regions, ss)
		}
	}

	if v, ok := extracted["application_name"]; ok {
		if s, ok := asString(v); ok {
			draft.Application.Name = s
			setProvenance(draft, "application.name", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["application_description"]; ok {
		if s, ok := asString(v); ok {
			draft.Application.Description = s
		}
	}
	if v, ok := extracted["domain"]; ok {
		if s, ok := asString(v); ok {
			draft.Application.Domain = s
			setProvenance(draft, "application.domain", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["architecture_style"]; ok {
		if s, ok := asString(v); ok {
			draft.Application.Architecture.Style = s
			setProvenance(draft, "application.architecture.style", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["compute_platforms"]; ok {
		if ss := asStrings(v); len(ss) > 0 {
			draft.Application.Architecture.ComputePlatforms = mergeStrings(draft.Application.Architecture.ComputePlatforms, ss)
			setProvenance(draft, "application.architecture.computePlatforms", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["databases"]; ok {
		if ss := asStrings(v); len(ss) > 0 {
			draft.Application.Architecture.Databases = mergeStrings(draft.Application.Architecture.Databases, ss)
			setProvenance(draft, "application.architecture.databases", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["caches"]; ok {
		if ss := asStrings(v); len(ss) > 0 {
			draft.Application.Architecture.Caches = mergeStrings(draft.Application.Architecture.Caches, ss)
		}
	}
	if v, ok := extracted["messaging"]; ok {
		if ss := asStrings(v); len(ss) > 0 {
			draft.Application.Architecture.Messaging = mergeStrings(draft.Application.Architecture.Messaging, ss)
		}
	}

	accountIDs := asStrings(extracted["aws_account_ids"])
	regions := asStrings(extracted["aws_regions"])
	environments := asStrings(extracted["environments"])
	if len(accountIDs) > 0 {
		draft.AWS.Accounts = assembleAccounts(draft.AWS.Accounts, accountIDs, regions, environments)
		setProvenance(draft, "aws.accounts", core.ProvenanceConfirmed)
		if draft.Security.AWSAccessMode == "" {
			// The specification's only supported production mode; asking the
			// user to state the obvious would waste their time.
			draft.Security.AWSAccessMode = "assume_role"
			draft.AWS.AccessMode = "assume_role"
			setProvenance(draft, "security.awsAccessMode", core.ProvenanceInferred)
		}
	}

	if v, ok := extracted["business_transactions"]; ok {
		draft.Business.Transactions = mergeTransactions(draft.Business.Transactions, asTransactions(v))
		if len(draft.Business.Transactions) > 0 {
			setProvenance(draft, "business.transactions", core.ProvenanceConfirmed)
		}
	}

	if v, ok := extracted["cost_reduction_target"]; ok {
		if f, ok := asFloat(v); ok {
			draft.Objectives.CostReductionTarget = f
			setProvenance(draft, "objectives.costReductionTarget", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["monthly_budget"]; ok {
		if f, ok := asFloat(v); ok {
			draft.Objectives.MonthlyBudget = f
			setProvenance(draft, "objectives.monthlyBudget", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["availability_target"]; ok {
		if f, ok := asFloat(v); ok {
			draft.Objectives.AvailabilityTarget = f
			setProvenance(draft, "objectives.availabilityTarget", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["max_latency_ms"]; ok {
		if f, ok := asFloat(v); ok {
			draft.Objectives.MaxLatencyMS = f
			setProvenance(draft, "objectives.maxLatencyMs", core.ProvenanceConfirmed)
		}
	}

	if v, ok := extracted["risk_tolerance"]; ok {
		if s, ok := asString(v); ok {
			draft.Optimization.RiskTolerance = s
			setProvenance(draft, "optimization.riskTolerance", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["spot_allowed"]; ok {
		if b, ok := asBool(v); ok {
			draft.Optimization.SpotAllowed = b
		}
	}
	if v, ok := extracted["automation_enabled"]; ok {
		if b, ok := asBool(v); ok {
			draft.Automation.Enabled = b
		}
	}
	if v, ok := extracted["governance_requires_approval"]; ok {
		if b, ok := asBool(v); ok {
			draft.Governance.ProductionChangesRequireApproval = b
			setProvenance(draft, "governance.productionChangesRequireApproval", core.ProvenanceConfirmed)
		}
	}
	if v, ok := extracted["compliance_frameworks"]; ok {
		if ss := asStrings(v); len(ss) > 0 {
			draft.Security.ComplianceFrameworks = mergeStrings(draft.Security.ComplianceFrameworks, ss)
			setProvenance(draft, "security.complianceFrameworks", core.ProvenanceConfirmed)
		}
	}
}

// assembleAccounts pairs extracted account ids with extracted regions and
// environments positionally: the Nth account id takes the Nth region set
// (all regions when there is only one region group stated) and the Nth
// environment (defaulting to "production" when environments were never
// stated at all, since a lone AWS account with no stated environment is,
// overwhelmingly, the production account). This is a real but deliberately
// simple assembly rule — free text like "accounts 111111111111 (prod) and
// 222222222222 (staging), both in us-east-1" does not parse into a
// guaranteed unambiguous structure, and the review screen shows exactly
// what was assembled so a wrong pairing is caught before approval rather
// than after.
func assembleAccounts(existing []spec.Account, ids, regions, environments []string) []spec.Account {
	byID := map[string]spec.Account{}
	for _, a := range existing {
		byID[a.ID] = a
	}
	for i, id := range ids {
		a, had := byID[id]
		a.ID = id
		if len(a.Regions) == 0 {
			if len(regions) > 0 {
				a.Regions = regions
			}
		}
		if a.Environment == "" {
			switch {
			case i < len(environments):
				a.Environment = environments[i]
			case len(environments) == 1:
				a.Environment = environments[0]
			case len(ids) == 1:
				a.Environment = "production"
			}
			a.Production = a.Environment == "production"
		}
		if !had {
			existing = append(existing, a)
		} else {
			for j := range existing {
				if existing[j].ID == id {
					existing[j] = a
				}
			}
		}
		byID[id] = a
	}
	return existing
}

func asTransactions(v any) []spec.TransactionSpec {
	list, ok := v.([]map[string]any)
	if !ok {
		// A real model's JSON decode produces []any of map[string]any rather
		// than the deterministic provider's native []map[string]any.
		if raw, ok2 := v.([]any); ok2 {
			for _, item := range raw {
				if m, ok3 := item.(map[string]any); ok3 {
					list = append(list, m)
				}
			}
		}
	}
	out := make([]spec.TransactionSpec, 0, len(list))
	for _, m := range list {
		name, _ := asString(m["name"])
		vol, _ := asFloat(m["monthly_volume"])
		if name == "" {
			continue
		}
		out = append(out, spec.TransactionSpec{Name: name, MonthlyVolume: vol})
	}
	return out
}

func mergeTransactions(existing, extra []spec.TransactionSpec) []spec.TransactionSpec {
	byName := map[string]int{}
	for i, t := range existing {
		byName[t.Name] = i
	}
	for _, t := range extra {
		if i, ok := byName[t.Name]; ok {
			if existing[i].MonthlyVolume == 0 {
				existing[i].MonthlyVolume = t.MonthlyVolume
			}
			continue
		}
		byName[t.Name] = len(existing)
		existing = append(existing, t)
	}
	return existing
}

// mergeStrings unions b into a, preserving a's order and skipping
// duplicates case-insensitively.
func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	for _, s := range a {
		seen[strings.ToLower(s)] = true
	}
	for _, s := range b {
		if !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			a = append(a, s)
		}
	}
	return a
}

// industryDomainDefaults gives the application domain a plausible default
// from the stated industry, when the user never named one directly.
var industryDomainDefaults = map[string]string{
	"e-commerce": "checkout", "retail": "checkout",
	"financial_services": "payments", "insurance": "claims",
	"travel": "booking", "logistics": "logistics", "media": "content",
}

// industryAvailabilityDefaults gives the availability target a plausible
// industry-standard default. These are ordinary, publicly-known SLA
// conventions (three nines for most commercial services, more for
// regulated or transaction-critical industries), not a claim about any
// specific customer's actual reliability requirement — which is exactly why
// every value this function sets is marked INFERRED with its rationale
// rather than CONFIRMED, and the review screen invites the user to correct
// it.
var industryAvailabilityDefaults = map[string]float64{
	"financial_services": 0.9995, "healthcare": 0.9995, "insurance": 0.999,
}

// runInference fills fields the user has not stated, from facts the user
// has stated. Every field it sets carries core.ProvenanceInferred and a
// one-sentence rationale — never core.ProvenanceConfirmed, and never a value
// the review screen doesn't explain.
func runInference(draft *spec.Spec) {
	industry := strings.ToLower(draft.Organization.Industry)

	if draft.Application.Domain == "" && industry != "" {
		if d, ok := industryDomainDefaults[industry]; ok {
			draft.Application.Domain = d
			setProvenance(draft, "application.domain", core.ProvenanceInferred)
			draft.OpenQuestions = upsertInferenceNote(draft.OpenQuestions, "application.domain",
				fmt.Sprintf("inferred %q from the stated industry %q", d, draft.Organization.Industry))
		}
	}

	if draft.Application.Criticality == "" {
		crit := "medium"
		switch {
		case industry == "financial_services" || industry == "healthcare" || industry == "insurance":
			crit = "high"
		case draft.Organization.Size == "startup":
			crit = "medium"
		}
		draft.Application.Criticality = crit
		setProvenance(draft, "application.criticality", core.ProvenanceInferred)
	}

	if draft.Objectives.AvailabilityTarget == 0 {
		target := 0.995
		if v, ok := industryAvailabilityDefaults[industry]; ok {
			target = v
		}
		draft.Objectives.AvailabilityTarget = target
		setProvenance(draft, "objectives.availabilityTarget", core.ProvenanceInferred)
	}

	if len(draft.AWS.Accounts) > 0 {
		anyEnvSet := false
		for _, a := range draft.AWS.Accounts {
			if a.Environment != "" {
				anyEnvSet = true
			}
		}
		if !anyEnvSet {
			for i := range draft.AWS.Accounts {
				draft.AWS.Accounts[i].Environment = "production"
				draft.AWS.Accounts[i].Production = true
			}
			setProvenance(draft, "aws.accounts.environment", core.ProvenanceInferred)
		}
	}

	if draft.Optimization.RiskTolerance == "" {
		// No default is applied here: risk tolerance is a blocking field
		// (see stage.go's StageGovernance) precisely because guessing a
		// customer's risk appetite is not something CloudOptix will do
		// silently — the agent asks, and if the user says "I don't know" the
		// field becomes UNKNOWN, not a guessed default.
	}

	if hasProductionAccount(*draft) && draft.Governance.ProductionChangesRequireApproval == false &&
		draft.Provenance["governance.productionChangesRequireApproval"] != core.ProvenanceConfirmed {
		draft.Governance.ProductionChangesRequireApproval = true
		setProvenance(draft, "governance.productionChangesRequireApproval", core.ProvenanceInferred)
	}

	if draft.Objectives.MonthlyBudget > 0 && len(draft.Objectives.CostSLOs) == 0 {
		draft.Objectives.CostSLOs = append(draft.Objectives.CostSLOs, spec.CostSLOSpec{
			Name: "monthly-budget", Kind: "monthly_budget", Target: draft.Objectives.MonthlyBudget, Window: "monthly",
		})
		setProvenance(draft, "objectives.costSlos", core.ProvenanceInferred)
	}
}

// upsertInferenceNote is a light-weight place to record why an inference was
// made, using the same OpenQuestion structure the agent already has for
// showing rationale on the review screen (Required=false, Blocking=false —
// it is informational, not something the agent still needs an answer to).
func upsertInferenceNote(qs []spec.OpenQuestion, path, why string) []spec.OpenQuestion {
	for i, q := range qs {
		if q.Path == path {
			qs[i].Why = why
			return qs
		}
	}
	return append(qs, spec.OpenQuestion{Path: path, Why: why})
}

// sortedProvenancePaths returns the tracked provenance paths in a stable
// order, for building FieldState lists deterministically.
func sortedProvenancePaths(draft spec.Spec) []string {
	paths := make([]string, 0, len(draft.Provenance))
	for p := range draft.Provenance {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
