package notify

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// rendered is the channel-agnostic content the Dispatcher produces from one
// ports.Event, before it is addressed to any particular channel. Subject
// and Body are always populated (every Notifier can fall back to them);
// Blocks carries channel-specific richness (an "html" string for email, a
// "blocks" Block Kit array for Slack) that a plain-text-only channel simply
// ignores.
type rendered struct {
	Subject  string
	Body     string
	Blocks   map[string]any
	Severity core.Severity
	LinkURL  string
}

// render dispatches to the template for e.Type, falling back to a generic
// rendering for any event type this package does not special-case — new
// event types are added to ports.EventType faster than every one of them
// earns a bespoke template, and an alert that reads "cloudoptix.foo.bar
// occurred" is far better than one silently dropped for want of a
// template.
func render(e ports.Event, linkBase string) rendered {
	link := payloadString(e, "link_url", "")
	if link == "" && linkBase != "" {
		link = linkBase
	}

	var r rendered
	switch e.Type {
	case ports.EventCostAnomalyDetected:
		r = renderCostAnomaly(e)
	case ports.EventCostSLOBreached:
		r = renderSLOViolation(e)
	case ports.EventRecommendationCreated:
		r = renderNewRecommendation(e)
	case ports.EventApprovalRequested:
		r = renderApprovalRequest(e)
	case ports.EventOptimizationExecuted, ports.EventOptimizationValidated:
		r = renderOptimizationCompleted(e)
	case ports.EventOptimizationRolledBack:
		r = renderRollback(e)
	case ports.EventCostRegressionDetected:
		r = renderCostRegression(e)
	default:
		r = renderGeneric(e)
	}
	if r.LinkURL == "" {
		r.LinkURL = link
	}
	return r
}

func renderCostAnomaly(e ports.Event) rendered {
	resource := payloadString(e, "resource_name", string(e.SubjectID))
	amount := payloadMoneyLike(e, "delta_amount")
	pct := payloadFloat(e, "delta_pct", 0)
	return rendered{
		Subject: fmt.Sprintf("Cost anomaly detected: %s", resource),
		Body: fmt.Sprintf(
			"CloudOptix detected an unexpected cost change on %s: %s (%.1f%%) above the expected baseline. Review the resource's recent activity for the cause.",
			resource, amount, pct,
		),
		Severity: severityFor(e, core.SeverityHigh),
	}
}

func renderSLOViolation(e ports.Event) rendered {
	sloName := payloadString(e, "slo_name", "cost SLO")
	detail := payloadString(e, "detail", "the configured threshold was exceeded")
	return rendered{
		Subject:  fmt.Sprintf("SLO breached: %s", sloName),
		Body:     fmt.Sprintf("%s has been breached. %s", sloName, detail),
		Severity: severityFor(e, core.SeverityHigh),
	}
}

func renderNewRecommendation(e ports.Event) rendered {
	resource := payloadString(e, "resource_name", string(e.SubjectID))
	savings := payloadMoneyLike(e, "estimated_monthly_savings")
	action := payloadString(e, "action_type", "an optimization")
	return rendered{
		Subject: fmt.Sprintf("New recommendation: %s on %s", action, resource),
		Body: fmt.Sprintf(
			"CloudOptix found a new optimization opportunity: %s on %s, estimated to save %s per month. Review it in CloudOptix to approve or dismiss.",
			action, resource, savings,
		),
		Severity: severityFor(e, core.SeverityInfo),
	}
}

func renderApprovalRequest(e ports.Event) rendered {
	requestedBy := payloadString(e, "requested_by", "CloudOptix")
	summary := payloadString(e, "summary", "a change awaiting your approval")
	return rendered{
		Subject:  "Approval requested",
		Body:     fmt.Sprintf("%s requested approval for: %s. This change will not proceed until it is approved.", requestedBy, summary),
		Severity: severityFor(e, core.SeverityMedium),
	}
}

func renderOptimizationCompleted(e ports.Event) rendered {
	resource := payloadString(e, "resource_name", string(e.SubjectID))
	savings := payloadMoneyLike(e, "realized_monthly_savings")
	verdict := payloadString(e, "verdict", "completed")
	return rendered{
		Subject: fmt.Sprintf("Optimization %s: %s", verdict, resource),
		Body: fmt.Sprintf(
			"The optimization on %s has %s. Realized savings so far: %s per month.",
			resource, verdict, savings,
		),
		Severity: severityFor(e, core.SeverityInfo),
	}
}

func renderRollback(e ports.Event) rendered {
	resource := payloadString(e, "resource_name", string(e.SubjectID))
	reason := payloadString(e, "reason", "a validation check failed")
	return rendered{
		Subject:  fmt.Sprintf("Change rolled back: %s", resource),
		Body:     fmt.Sprintf("CloudOptix rolled back the change on %s because %s. The resource has been returned to its prior configuration.", resource, reason),
		Severity: severityFor(e, core.SeverityHigh),
	}
}

func renderCostRegression(e ports.Event) rendered {
	resource := payloadString(e, "resource_name", string(e.SubjectID))
	amount := payloadMoneyLike(e, "regression_amount")
	return rendered{
		Subject: fmt.Sprintf("Cost regression detected: %s", resource),
		Body: fmt.Sprintf(
			"A previously realized saving on %s has regressed: cost increased by %s since validation. This may mean the optimization did not hold, or the resource's workload changed.",
			resource, amount,
		),
		Severity: severityFor(e, core.SeverityCritical),
	}
}

func renderGeneric(e ports.Event) rendered {
	return rendered{
		Subject:  fmt.Sprintf("CloudOptix: %s", e.Type),
		Body:     fmt.Sprintf("Event %s occurred for subject %s.", e.Type, e.SubjectID),
		Severity: severityFor(e, core.SeverityInfo),
	}
}

// severityFor reads an explicit "severity" payload key when the event
// producer set one, otherwise falls back to fallback — the per-template
// default for that event type. This lets a producer override severity
// per-occurrence (e.g. a cost anomaly that is merely notable versus one
// that is a clear billing error) without every template needing its own
// bespoke severity-derivation logic.
func severityFor(e ports.Event, fallback core.Severity) core.Severity {
	s := payloadString(e, "severity", "")
	switch core.Severity(s) {
	case core.SeverityInfo, core.SeverityLow, core.SeverityMedium, core.SeverityHigh, core.SeverityCritical:
		return core.Severity(s)
	default:
		return fallback
	}
}

func payloadString(e ports.Event, key, fallback string) string {
	if e.Payload == nil {
		return fallback
	}
	v, ok := e.Payload[key]
	if !ok {
		return fallback
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return fallback
		}
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}

func payloadFloat(e ports.Event, key string, fallback float64) float64 {
	if e.Payload == nil {
		return fallback
	}
	v, ok := e.Payload[key]
	if !ok {
		return fallback
	}
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	default:
		return fallback
	}
}

// payloadMoneyLike renders a payload value that may be a core.Money (when
// the producer is in-process and populated Payload directly), a
// map[string]any (when the event round-tripped through JSON — Money
// marshals to {micros, currency, amount, display}, see
// core.Money.MarshalJSON — in which case the "display" field is already the
// human-readable form), a bare float64, or a string already formatted for
// display. Any other shape or a missing key renders as "an unknown amount"
// rather than panicking or silently printing "0".
func payloadMoneyLike(e ports.Event, key string) string {
	if e.Payload == nil {
		return "an unknown amount"
	}
	v, ok := e.Payload[key]
	if !ok {
		return "an unknown amount"
	}
	switch t := v.(type) {
	case core.Money:
		return t.Format()
	case map[string]any:
		if disp, ok := t["display"].(string); ok && disp != "" {
			return disp
		}
		if amt, ok := t["amount"].(float64); ok {
			return fmt.Sprintf("$%.2f", amt)
		}
		return "an unknown amount"
	case string:
		if t == "" {
			return "an unknown amount"
		}
		return t
	case float64:
		return fmt.Sprintf("$%.2f", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
