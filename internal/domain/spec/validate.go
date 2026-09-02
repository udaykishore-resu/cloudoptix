package spec

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// CurrentAPIVersion is the specification schema version this build writes.
const CurrentAPIVersion = "cloudoptix.io/v1"

// KindSpec is the specification document kind.
const KindSpec = "CloudOptixSpec"

var (
	accountRe = regexp.MustCompile(`^\d{12}$`)
	regionRe  = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-\d$`)
	arnRoleRe = regexp.MustCompile(`^arn:aws[a-z-]*:iam::\d{12}:role/.+$`)
	emailRe   = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	timeRe    = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
)

var validEnvironments = map[string]bool{
	"production": true, "staging": true, "development": true,
	"test": true, "sandbox": true, "dr": true,
}

var validRiskTolerance = map[string]bool{"low": true, "medium": true, "high": true}

// Validate performs the deterministic validation that stands between a draft
// specification and a tenant being created.
//
// This is where the conversational, forgiving half of onboarding meets the
// exact half. The agent is allowed to be relaxed; this function is not. Every
// check here is one a mistake in would cost the customer money or safety:
// a malformed account id means discovery finds nothing; a missing external id
// means a confused-deputy exposure; automation enabled with no maintenance
// window means changes at peak traffic.
//
// Traceability: REQ-SPEC-008, SPEC-ONB-004.
func (s Spec) Validate() core.ValidationResult {
	var v core.ValidationResult

	if s.APIVersion == "" {
		v.Add("apiVersion", "required", core.SeverityHigh, "apiVersion is required")
	} else if s.APIVersion != CurrentAPIVersion {
		v.AddHint("apiVersion", "unsupported", core.SeverityHigh,
			"set apiVersion to "+CurrentAPIVersion,
			"apiVersion %q is not supported by this build", s.APIVersion)
	}
	if s.Kind != "" && s.Kind != KindSpec {
		v.Add("kind", "invalid", core.SeverityHigh, "kind must be %s", KindSpec)
	}

	// Organization and application.
	if strings.TrimSpace(s.Organization.Name) == "" {
		v.Add("organization.name", "required", core.SeverityCritical, "organization name is required")
	}
	if strings.TrimSpace(s.Application.Name) == "" {
		v.Add("application.name", "required", core.SeverityCritical, "application name is required")
	}

	// AWS accounts.
	if len(s.AWS.Accounts) == 0 {
		v.Add("aws.accounts", "required", core.SeverityCritical,
			"at least one AWS account is required; CloudOptix has nothing to analyse without one")
	}
	seenAccounts := map[string]bool{}
	hasProduction := false
	for i, a := range s.AWS.Accounts {
		p := fmt.Sprintf("aws.accounts[%d]", i)
		if !accountRe.MatchString(a.ID) {
			v.Add(p+".id", "invalid", core.SeverityCritical,
				"account id %q must be exactly 12 digits", a.ID)
		}
		if seenAccounts[a.ID] {
			v.Add(p+".id", "duplicate", core.SeverityHigh, "account %s appears more than once", a.ID)
		}
		seenAccounts[a.ID] = true

		if a.Environment == "" {
			v.Add(p+".environment", "required", core.SeverityHigh,
				"each account must declare its environment so production changes are governed correctly")
		} else if !validEnvironments[strings.ToLower(a.Environment)] {
			v.AddHint(p+".environment", "invalid", core.SeverityMedium,
				"use one of: production, staging, development, test, sandbox, dr",
				"environment %q is not recognised", a.Environment)
		}
		if strings.EqualFold(a.Environment, "production") || a.Production {
			hasProduction = true
		}
		if len(a.Regions) == 0 {
			v.Add(p+".regions", "required", core.SeverityHigh, "at least one region is required")
		}
		for j, r := range a.Regions {
			if !regionRe.MatchString(r) {
				v.Add(fmt.Sprintf("%s.regions[%d]", p, j), "invalid", core.SeverityMedium,
					"%q does not look like an AWS region code", r)
			}
		}
		if s.AWS.AccessMode == "assume_role" || s.Security.AWSAccessMode == "assume_role" {
			if a.RoleARN == "" {
				v.Add(p+".roleArn", "required", core.SeverityCritical,
					"an IAM role ARN is required; CloudOptix never accepts long-lived access keys")
			} else if !arnRoleRe.MatchString(a.RoleARN) {
				v.Add(p+".roleArn", "invalid", core.SeverityHigh, "%q is not a valid IAM role ARN", a.RoleARN)
			}
			if a.ExternalID == "" {
				v.AddHint(p+".externalId", "required", core.SeverityCritical,
					"CloudOptix generates the external ID during account onboarding",
					"an external ID is required to prevent the confused-deputy attack")
			}
		}
	}

	mode := s.Security.AWSAccessMode
	if mode == "" {
		mode = s.AWS.AccessMode
	}
	switch mode {
	case "assume_role", "simulated":
	case "":
		v.Add("security.awsAccessMode", "required", core.SeverityHigh,
			"access mode is required; the only supported production mode is assume_role")
	default:
		v.AddHint("security.awsAccessMode", "unsupported", core.SeverityCritical,
			"use assume_role",
			"access mode %q is not supported; CloudOptix never stores AWS access keys", mode)
	}

	// Objectives.
	if s.Objectives.CostReductionTarget < 0 || s.Objectives.CostReductionTarget > 0.9 {
		if s.Objectives.CostReductionTarget != 0 {
			v.AddHint("objectives.costReductionTarget", "implausible", core.SeverityMedium,
				"express the target as a fraction, e.g. 0.25 for 25%",
				"a cost reduction target of %.2f is outside the plausible 0..0.9 range",
				s.Objectives.CostReductionTarget)
		}
	}
	if s.Objectives.AvailabilityTarget != 0 &&
		(s.Objectives.AvailabilityTarget <= 0.5 || s.Objectives.AvailabilityTarget >= 1) {
		v.AddHint("objectives.availabilityTarget", "implausible", core.SeverityMedium,
			"express availability as a fraction, e.g. 0.9999",
			"availability target %.4f should be between 0.5 and 1", s.Objectives.AvailabilityTarget)
	}
	// A high availability target combined with high risk tolerance is a
	// contradiction the customer should resolve before CloudOptix acts on
	// either.
	if s.Objectives.AvailabilityTarget >= 0.9999 && strings.EqualFold(s.Optimization.RiskTolerance, "high") {
		v.AddHint("optimization.riskTolerance", "conflicting_objectives", core.SeverityMedium,
			"lower the risk tolerance or relax the availability target",
			"a four-nines availability target sits awkwardly with a high risk tolerance")
	}
	for i, slo := range s.Objectives.CostSLOs {
		p := fmt.Sprintf("objectives.costSlos[%d]", i)
		if slo.Name == "" {
			v.Add(p+".name", "required", core.SeverityMedium, "cost SLO name is required")
		}
		if slo.Target <= 0 {
			v.Add(p+".target", "invalid", core.SeverityHigh, "cost SLO target must be positive")
		}
		if slo.Kind == "cost_per_transaction" && slo.Transaction == "" {
			v.Add(p+".transaction", "required", core.SeverityHigh,
				"a per-transaction cost SLO must name the transaction it measures")
		}
		if slo.ErrorBudgetPct < 0 || slo.ErrorBudgetPct > 0.5 {
			if slo.ErrorBudgetPct != 0 {
				v.Add(p+".errorBudgetPct", "implausible", core.SeverityMedium,
					"an economic error budget of %.0f%% is outside the sensible 0-50%% range",
					slo.ErrorBudgetPct*100)
			}
		}
	}

	// Optimization posture.
	if s.Optimization.RiskTolerance == "" {
		v.Add("optimization.riskTolerance", "required", core.SeverityMedium,
			"risk tolerance drives which optimizations are proposed at all")
	} else if !validRiskTolerance[strings.ToLower(s.Optimization.RiskTolerance)] {
		v.Add("optimization.riskTolerance", "invalid", core.SeverityMedium,
			"risk tolerance must be low, medium or high")
	}

	// Automation is the highest-consequence section, so its checks are the
	// strictest in the file.
	if s.Automation.Enabled {
		if !s.Governance.ProductionChangesRequireApproval && hasProduction {
			v.AddHint("governance.productionChangesRequireApproval", "unsafe_combination", core.SeverityHigh,
				"require approval for production changes, or restrict automation to non-production",
				"automation is enabled against a production account with no approval requirement")
		}
		if len(s.Automation.MaintenanceWindows) == 0 && hasProduction {
			v.AddHint("automation.maintenanceWindows", "missing_window", core.SeverityHigh,
				"define at least one maintenance window",
				"automation against production with no maintenance window permits changes at peak traffic")
		}
		if s.Automation.ValidationWindowMinutes > 0 && s.Automation.ValidationWindowMinutes < 15 {
			v.AddHint("automation.validationWindowMinutes", "too_short", core.SeverityMedium,
				"use at least 15 minutes",
				"a validation window of %d minutes is too short to observe a regression",
				s.Automation.ValidationWindowMinutes)
		}
		if !s.Automation.AutoRollback {
			v.AddHint("automation.autoRollback", "disabled", core.SeverityMedium,
				"enable autoRollback",
				"automation without automatic rollback means a regression waits for a human")
		}
		if s.Automation.MaxConcurrentChanges > 10 {
			v.Add("automation.maxConcurrentChanges", "too_high", core.SeverityMedium,
				"%d concurrent automated changes makes a regression hard to attribute",
				s.Automation.MaxConcurrentChanges)
		}
	}

	for i, w := range s.Automation.MaintenanceWindows {
		p := fmt.Sprintf("automation.maintenanceWindows[%d]", i)
		if !timeRe.MatchString(w.StartUTC) {
			v.Add(p+".startUtc", "invalid", core.SeverityMedium, "start time %q must be HH:MM UTC", w.StartUTC)
		}
		if w.DurationMinutes <= 0 {
			v.Add(p+".durationMinutes", "invalid", core.SeverityMedium, "duration must be positive")
		}
		if len(w.Days) == 0 {
			v.Add(p+".days", "required", core.SeverityLow, "a maintenance window needs at least one day")
		}
	}

	// Business transactions: the denominators.
	txNames := map[string]bool{}
	for i, t := range s.Business.Transactions {
		p := fmt.Sprintf("business.transactions[%d]", i)
		if t.Name == "" {
			v.Add(p+".name", "required", core.SeverityMedium, "transaction name is required")
		}
		if txNames[t.Name] {
			v.Add(p+".name", "duplicate", core.SeverityMedium, "transaction %q is declared twice", t.Name)
		}
		txNames[t.Name] = true
		if t.MonthlyVolume < 0 {
			v.Add(p+".monthlyVolume", "invalid", core.SeverityMedium, "monthly volume cannot be negative")
		}
		if t.MonthlyVolume == 0 && t.VolumeMetric == "" {
			v.AddHint(p+".monthlyVolume", "no_denominator", core.SeverityLow,
				"declare a monthly volume or point at a CloudWatch metric",
				"transaction %q has no volume source, so no cost-per-transaction can be computed", t.Name)
		}
	}
	for i, slo := range s.Objectives.CostSLOs {
		if slo.Transaction != "" && !txNames[slo.Transaction] {
			v.Add(fmt.Sprintf("objectives.costSlos[%d].transaction", i), "unresolved", core.SeverityHigh,
				"cost SLO references transaction %q which is not declared", slo.Transaction)
		}
	}

	// Workload cross-references.
	wlNames := map[string]bool{}
	for _, w := range s.Workloads {
		wlNames[w.Name] = true
	}
	for i, w := range s.Workloads {
		p := fmt.Sprintf("workloads[%d]", i)
		if w.Name == "" {
			v.Add(p+".name", "required", core.SeverityMedium, "workload name is required")
		}
		for j, dep := range w.DependsOn {
			if !wlNames[dep] {
				v.Add(fmt.Sprintf("%s.dependsOn[%d]", p, j), "unresolved", core.SeverityLow,
					"workload %q depends on %q which is not declared", w.Name, dep)
			}
		}
		if w.Environment != "" && !validEnvironments[strings.ToLower(w.Environment)] {
			v.Add(p+".environment", "invalid", core.SeverityLow, "environment %q is not recognised", w.Environment)
		}
	}
	for i, t := range s.Business.Transactions {
		for j, wl := range t.Workloads {
			if len(wlNames) > 0 && !wlNames[wl] {
				v.Add(fmt.Sprintf("business.transactions[%d].workloads[%d]", i, j), "unresolved",
					core.SeverityLow, "transaction %q references undeclared workload %q", t.Name, wl)
			}
		}
	}

	// Teams and members.
	for i, t := range s.Teams {
		for j, m := range t.Members {
			p := fmt.Sprintf("teams[%d].members[%d]", i, j)
			if !emailRe.MatchString(m.Email) {
				v.Add(p+".email", "invalid", core.SeverityMedium, "%q is not a valid email address", m.Email)
			}
			if len(m.Roles) == 0 {
				v.Add(p+".roles", "required", core.SeverityMedium, "member %s has no roles", m.Email)
			}
			for k, r := range m.Roles {
				if !core.Role(r).Valid() {
					v.Add(fmt.Sprintf("%s.roles[%d]", p, k), "invalid", core.SeverityMedium,
						"%q is not a CloudOptix role", r)
				}
			}
		}
	}

	// Notification channels must not carry inline secrets: the specification
	// is designed to live in a customer's git repository.
	for i, c := range s.Notifications.Channels {
		p := fmt.Sprintf("notifications.channels[%d]", i)
		if c.Name == "" {
			v.Add(p+".name", "required", core.SeverityLow, "channel name is required")
		}
		switch c.Type {
		case "email", "slack", "webhook":
		default:
			v.Add(p+".type", "invalid", core.SeverityMedium, "channel type %q is not supported", c.Type)
		}
		if looksLikeSecret(c.Target) {
			v.AddHint(p+".target", "inline_secret", core.SeverityCritical,
				"move the value into a secret and reference it with secretRef",
				"this channel target looks like a credential and must not be stored in the specification")
		}
	}

	return v
}

func looksLikeSecret(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "hooks.slack.com/services/") ||
		strings.HasPrefix(l, "xoxb-") || strings.HasPrefix(l, "xoxp-") ||
		strings.Contains(l, "token=") || strings.Contains(l, "secret=")
}

// AssessCompleteness scores how much of the specification is known and decides
// whether it can go to review. The blocking-gap list is what the onboarding
// agent works through, and it is derived here rather than in the agent so that
// a specification uploaded as YAML is judged by exactly the same standard as
// one produced by conversation.
func (s Spec) AssessCompleteness() Completeness {
	type check struct {
		path     string
		known    bool
		blocking bool
		label    string
	}
	hasProd := false
	for _, a := range s.AWS.Accounts {
		if a.Production || strings.EqualFold(a.Environment, "production") {
			hasProd = true
		}
	}
	checks := []check{
		{"organization.name", s.Organization.Name != "", true, "organization name"},
		{"organization.industry", s.Organization.Industry != "", false, "industry"},
		{"application.name", s.Application.Name != "", true, "application name"},
		{"application.domain", s.Application.Domain != "", false, "business domain"},
		{"application.architecture.style", s.Application.Architecture.Style != "", false, "architecture style"},
		{"application.architecture.computePlatforms", len(s.Application.Architecture.ComputePlatforms) > 0, false, "compute platforms"},
		{"application.architecture.databases", len(s.Application.Architecture.Databases) > 0, false, "databases"},
		{"aws.accounts", len(s.AWS.Accounts) > 0, true, "AWS accounts"},
		{"aws.accessMode", s.Security.AWSAccessMode != "" || s.AWS.AccessMode != "", true, "AWS access mode"},
		{"workloads", len(s.Workloads) > 0, false, "workloads"},
		{"business.transactions", len(s.Business.Transactions) > 0, false, "business transactions"},
		{"objectives.costReductionTarget", s.Objectives.CostReductionTarget > 0, false, "cost reduction target"},
		{"objectives.monthlyBudget", s.Objectives.MonthlyBudget > 0, false, "monthly budget"},
		{"objectives.availabilityTarget", s.Objectives.AvailabilityTarget > 0, false, "availability target"},
		{"objectives.maxLatencyMs", s.Objectives.MaxLatencyMS > 0, false, "latency objective"},
		{"optimization.riskTolerance", s.Optimization.RiskTolerance != "", true, "risk tolerance"},
		{"automation.enabled", true, false, "automation preference"},
		{"governance.productionChangesRequireApproval", !hasProd || s.Governance.ProductionChangesRequireApproval || !s.Automation.Enabled, hasProd, "production approval policy"},
		{"security.complianceFrameworks", len(s.Security.ComplianceFrameworks) > 0, false, "compliance requirements"},
		{"notifications.channels", len(s.Notifications.Channels) > 0, false, "notification channels"},
		{"teams", len(s.Teams) > 0, false, "teams and ownership"},
	}

	c := Completeness{TotalFields: len(checks)}
	for _, ch := range checks {
		prov := s.Provenance[ch.path]
		switch {
		case !ch.known:
			c.Unknown++
			if ch.blocking {
				c.BlockingGaps = append(c.BlockingGaps, ch.label)
			}
		case prov == core.ProvenanceInferred:
			c.Inferred++
		case prov == core.ProvenanceRequiresConfirmation:
			c.NeedsConfirmation++
		default:
			c.Confirmed++
		}
	}
	// Confirmed facts count fully, inferred at 70%, needs-confirmation at 40%.
	// The weighting is what makes the progress bar honest: a specification
	// entirely assembled by inference is not a complete specification.
	if c.TotalFields > 0 {
		c.Score = (float64(c.Confirmed) + 0.7*float64(c.Inferred) + 0.4*float64(c.NeedsConfirmation)) /
			float64(c.TotalFields)
	}
	c.ReadyForReview = len(c.BlockingGaps) == 0
	return c
}
