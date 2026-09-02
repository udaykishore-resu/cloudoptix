package onboarding

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// markUnknown resolves every currently-outstanding question in stage as
// core.ProvenanceUnknown — the handler for a user replying "I don't know" to
// a batch of questions. It returns the paths it resolved, so the caller can
// report back plainly what it is moving on from.
func markUnknown(draft *spec.Spec, stage string) []string {
	outstanding := outstandingInStage(stage, *draft)
	paths := make([]string, 0, len(outstanding))
	for _, f := range outstanding {
		setProvenance(draft, f.Path, core.ProvenanceUnknown)
		paths = append(paths, f.Path)
	}
	return paths
}

// showSummaryPhrases trigger the "show me what you know" interaction: the
// agent stops asking questions for this turn and instead states its current
// understanding of the specification.
var showSummaryPhrases = []string{
	"show me what you know", "what do you know so far", "what do you know about",
	"summarize what you have", "show summary", "show me the summary",
	"what have you got so far", "recap what you know",
}

func isShowSummaryRequest(msg string) bool {
	lower := strings.ToLower(msg)
	for _, p := range showSummaryPhrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// directEditPattern is one recognised "change X to Y" instruction: a regexp
// whose captured value is parsed by Parse and applied by Apply.
type directEditPattern struct {
	Path  string
	Label string
	Re    *regexp.Regexp
	Apply func(draft *spec.Spec, raw string) (before, after string, err error)
}

var directEditPatterns = []directEditPattern{
	{
		Path: "objectives.availabilityTarget", Label: "availability target",
		Re: regexp.MustCompile(`(?i)(?:change|set|update)\s+(?:the\s+)?(?:production\s+)?(?:availability\s+target|slo|sla)\s+to\s+([\d.]+)\s*%?`),
		Apply: func(draft *spec.Spec, raw string) (string, string, error) {
			pct, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return "", "", err
			}
			before := fmt.Sprintf("%.4f", draft.Objectives.AvailabilityTarget)
			draft.Objectives.AvailabilityTarget = pct / 100
			setProvenance(draft, "objectives.availabilityTarget", core.ProvenanceConfirmed)
			return before, fmt.Sprintf("%.4f", draft.Objectives.AvailabilityTarget), nil
		},
	},
	{
		Path: "objectives.costReductionTarget", Label: "cost reduction target",
		Re: regexp.MustCompile(`(?i)(?:change|set|update)\s+(?:the\s+)?cost\s+reduction\s+target\s+to\s+([\d.]+)\s*%?`),
		Apply: func(draft *spec.Spec, raw string) (string, string, error) {
			pct, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return "", "", err
			}
			before := fmt.Sprintf("%.2f", draft.Objectives.CostReductionTarget)
			draft.Objectives.CostReductionTarget = pct / 100
			setProvenance(draft, "objectives.costReductionTarget", core.ProvenanceConfirmed)
			return before, fmt.Sprintf("%.2f", draft.Objectives.CostReductionTarget), nil
		},
	},
	{
		Path: "objectives.monthlyBudget", Label: "monthly budget",
		Re: regexp.MustCompile(`(?i)(?:change|set|update)\s+(?:the\s+)?monthly\s+budget\s+to\s+\$?\s?([\d,]+)`),
		Apply: func(draft *spec.Spec, raw string) (string, string, error) {
			v, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
			if err != nil {
				return "", "", err
			}
			before := fmt.Sprintf("%.0f", draft.Objectives.MonthlyBudget)
			draft.Objectives.MonthlyBudget = v
			setProvenance(draft, "objectives.monthlyBudget", core.ProvenanceConfirmed)
			return before, fmt.Sprintf("%.0f", draft.Objectives.MonthlyBudget), nil
		},
	},
	{
		Path: "optimization.riskTolerance", Label: "risk tolerance",
		Re: regexp.MustCompile(`(?i)(?:change|set|update)\s+(?:the\s+)?risk\s+tolerance\s+to\s+(low|medium|high)`),
		Apply: func(draft *spec.Spec, raw string) (string, string, error) {
			before := draft.Optimization.RiskTolerance
			draft.Optimization.RiskTolerance = strings.ToLower(raw)
			setProvenance(draft, "optimization.riskTolerance", core.ProvenanceConfirmed)
			return before, draft.Optimization.RiskTolerance, nil
		},
	},
	{
		Path: "automation.enabled", Label: "automation",
		Re: regexp.MustCompile(`(?i)(?:change|set|update|turn)\s+(?:the\s+)?automation\s+(?:to\s+)?(on|off|enabled|disabled)`),
		Apply: func(draft *spec.Spec, raw string) (string, string, error) {
			before := fmt.Sprintf("%v", draft.Automation.Enabled)
			draft.Automation.Enabled = strings.EqualFold(raw, "on") || strings.EqualFold(raw, "enabled")
			return before, fmt.Sprintf("%v", draft.Automation.Enabled), nil
		},
	},
}

// applyDirectEdit tries every recognised direct-edit pattern against
// message. It applies at most one edit per call (the first pattern that
// matches) and returns the resulting spec.Change for the reply to quote —
// this is what lets a review-screen-style command like "change production
// SLO to 99.99%" work from inside the conversation, with the same diff
// semantics ApplyEdit uses.
func applyDirectEdit(draft *spec.Spec, message string) (spec.Change, bool) {
	for _, p := range directEditPatterns {
		m := p.Re.FindStringSubmatch(message)
		if m == nil {
			continue
		}
		before, after, err := p.Apply(draft, m[1])
		if err != nil {
			continue
		}
		return spec.Change{
			Path: p.Path, Kind: spec.ChangeModified, Before: before, After: after,
			Severity: core.SeverityMedium,
			Impact:   fmt.Sprintf("%s updated directly from the conversation", p.Label),
		}, true
	}
	return spec.Change{}, false
}
