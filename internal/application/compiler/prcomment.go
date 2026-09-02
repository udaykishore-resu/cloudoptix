package compiler

import (
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// topMoversLimit caps how many changed resources the "top movers" table
// shows, so a thousand-resource plan produces a PR comment a human will
// actually read rather than a wall of rows.
const topMoversLimit = 10

// RenderPRComment produces the Markdown block CI posts on the pull request:
// the verdict, the delta table, the top movers, the risks and the required
// action. report may be nil (a compile with no regression suite run against
// it yet), in which case the comment shows the pricing result alone with no
// pass/fail verdict.
func RenderPRComment(result simulate.CompilationResult, report *simulate.RegressionReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## %s CloudOptix Cost Compiler\n\n", verdictIcon(report))
	if report != nil {
		fmt.Fprintf(&b, "**Verdict: %s** — %s\n\n", report.Verdict, report.Summary)
	} else {
		fmt.Fprintf(&b, "%s\n\n", headlineSentence(result))
	}

	b.WriteString("| | Monthly | Annual |\n|---|---:|---:|\n")
	fmt.Fprintf(&b, "| Baseline | %s | %s |\n", result.BaselineMonthly.Format(), result.BaselineMonthly.Annualized().Format())
	fmt.Fprintf(&b, "| Projected | %s | %s |\n", result.ProjectedMonthly.Format(), result.ProjectedMonthly.Annualized().Format())
	fmt.Fprintf(&b, "| **Delta** | **%s** (%+.1f%%) | **%s** |\n\n", signedFormat(result.MonthlyDelta), result.DeltaPct, signedFormat(result.AnnualDelta))

	fmt.Fprintf(&b, "Coverage: **%.0f%%** priced (%d unpriced of %d changes) · Confidence: **%s**\n\n",
		result.Coverage*100, result.UnpricedCount, len(result.Changes), result.Confidence)

	if movers := topMovers(result.Changes); len(movers) > 0 {
		b.WriteString("### Top movers\n\n| Resource | Action | Monthly Δ |\n|---|---|---:|\n")
		for _, c := range movers {
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", c.Address, c.Action, signedFormat(c.MonthlyDelta))
		}
		b.WriteString("\n")
	}

	if unpriced := unpricedChanges(result.Changes); len(unpriced) > 0 {
		b.WriteString("### Unpriced resources\n\n| Resource | Reason |\n|---|---|\n")
		for _, c := range unpriced {
			fmt.Fprintf(&b, "| `%s` | %s |\n", c.Address, c.UnpricedReason)
		}
		b.WriteString("\n")
	}

	if len(result.Risks) > 0 {
		b.WriteString("### Risks\n\n")
		for _, risk := range result.Risks {
			fmt.Fprintf(&b, "- %s **[%s]** %s", severityIcon(risk.Severity), risk.Code, risk.Summary)
			if !risk.MonthlyImpact.IsZero() {
				fmt.Fprintf(&b, " — %s/mo impact", risk.MonthlyImpact.Format())
			}
			b.WriteString("\n")
			if risk.Remediation != "" {
				fmt.Fprintf(&b, "  _Remediation: %s_\n", risk.Remediation)
			}
		}
		b.WriteString("\n")
	}

	if len(result.Opportunities) > 0 {
		b.WriteString("### Opportunities\n\n")
		for _, opp := range result.Opportunities {
			fmt.Fprintf(&b, "- `%s`: %s — save **%s/mo**\n", opp.Address, opp.Summary, opp.MonthlySaving.Format())
			fmt.Fprintf(&b, "  _Change: %s_\n", opp.Change)
		}
		b.WriteString("\n")
	}

	if report != nil && len(report.Results) > 0 {
		b.WriteString("### Cost regression checks\n\n| Check | Verdict | Expected | Actual |\n|---|---|---|---|\n")
		for _, res := range report.Results {
			fmt.Fprintf(&b, "| %s | %s %s | %s | %s |\n", res.Name, verdictEmoji(res.Verdict), res.Verdict, res.Expected, res.Actual)
		}
		b.WriteString("\n")
		for _, res := range report.Results {
			if res.Verdict != simulate.VerdictPass {
				fmt.Fprintf(&b, "> %s **%s**: %s\n", verdictEmoji(res.Verdict), res.Name, res.Message)
			}
		}
		if report.RequiredAction != "" {
			fmt.Fprintf(&b, "\n**Required action:** %s\n", report.RequiredAction)
		}
	}

	return b.String()
}

func headlineSentence(result simulate.CompilationResult) string {
	if result.MonthlyDelta.IsZero() {
		return "No net monthly cost change."
	}
	direction := "increase"
	if result.MonthlyDelta.IsNegative() {
		direction = "decrease"
	}
	return fmt.Sprintf("Monthly cost would %s by %s (%.1f%%).", direction, result.MonthlyDelta.Abs().Format(), result.DeltaPct)
}

func signedFormat(m core.Money) string {
	if m.IsNegative() || m.IsZero() {
		return m.Format()
	}
	return "+" + m.Format()
}

func topMovers(changes []simulate.PricedChange) []simulate.PricedChange {
	// Summarize() already sorts Changes by |MonthlyDelta| descending, so the
	// first N are the top movers by construction; a resource with exactly
	// zero delta (an update that did not change price, or a free resource)
	// is not a mover and is excluded even if it sorts near the front.
	var out []simulate.PricedChange
	for _, c := range changes {
		if c.MonthlyDelta.IsZero() {
			continue
		}
		out = append(out, c)
		if len(out) == topMoversLimit {
			break
		}
	}
	return out
}

func unpricedChanges(changes []simulate.PricedChange) []simulate.PricedChange {
	var out []simulate.PricedChange
	for _, c := range changes {
		if c.Unpriced {
			out = append(out, c)
		}
	}
	return out
}

func verdictIcon(report *simulate.RegressionReport) string {
	if report == nil {
		return "💰"
	}
	switch report.Verdict {
	case simulate.VerdictPass:
		return "✅"
	case simulate.VerdictWarning:
		return "⚠️"
	case simulate.VerdictFail:
		return "🛑"
	default:
		return "💰"
	}
}

func verdictEmoji(v simulate.Verdict) string {
	switch v {
	case simulate.VerdictPass:
		return "✅"
	case simulate.VerdictWarning:
		return "⚠️"
	case simulate.VerdictFail:
		return "🛑"
	default:
		return "•"
	}
}

func severityIcon(s core.Severity) string {
	switch s {
	case core.SeverityCritical:
		return "🔴"
	case core.SeverityHigh:
		return "🟠"
	case core.SeverityMedium:
		return "🟡"
	case core.SeverityLow:
		return "🔵"
	default:
		return "⚪"
	}
}
