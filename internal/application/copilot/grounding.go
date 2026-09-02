package copilot

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// resourceTokenRe finds anything in an answer that looks like a resource,
// account or recommendation identifier: native AWS ids (i-, vol-, sg-,
// vpc-, subnet-, nat-, ami-, snap-), CloudOptix ids (res_, rec_...), and
// bare 12-digit account numbers. It intentionally mirrors the identifier
// shapes deterministic/toolmatch.go already recognizes, so what the
// deterministic provider can extract from a question and what this
// verifier checks in an answer use the same vocabulary of "looks like an
// id".
var resourceTokenRe = regexp.MustCompile(`\b(?:i-[0-9a-zA-Z]{6,40}|vol-[0-9a-zA-Z]{6,40}|snap-[0-9a-zA-Z]{6,40}|ami-[0-9a-zA-Z]{6,40}|sg-[0-9a-zA-Z]{6,40}|vpc-[0-9a-zA-Z]{6,40}|subnet-[0-9a-zA-Z]{6,40}|nat-[0-9a-zA-Z]{6,40}|(?:res|rec)_[0-9a-zA-Z_]{6,}|\d{12})\b`)

// moneyTokenRe finds dollar figures: $1,234, $1,234.56, $12.
var moneyTokenRe = regexp.MustCompile(`\$([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)

// Verifier is the copilot's ports.GroundingVerifier: every resource-like
// identifier and every dollar figure the answer states is checked against
// the GroundingSet assembled from what the tool calls in this turn actually
// returned. Nothing here re-derives facts from the tenant's data itself —
// this is deliberately a closed check against what was already retrieved,
// so an answer cannot "ground" itself by being coincidentally correct about
// something no tool call surfaced.
type Verifier struct{}

// NewVerifier builds the copilot's GroundingVerifier.
func NewVerifier() *Verifier { return &Verifier{} }

var _ ports.GroundingVerifier = (*Verifier)(nil)

// Verify implements ports.GroundingVerifier.
func (Verifier) Verify(_ context.Context, _ core.TenantID, answer string, allowed ports.GroundingSet) (ports.GroundingReport, error) {
	report := ports.GroundingReport{Grounded: true, Confidence: 1}

	var checkable, resolved int

	for _, tok := range dedupe(resourceTokenRe.FindAllString(answer, -1)) {
		checkable++
		if resourceKnown(tok, allowed) {
			resolved++
			continue
		}
		report.UnknownResources = append(report.UnknownResources, tok)
	}

	for _, m := range moneyTokenRe.FindAllStringSubmatch(answer, -1) {
		checkable++
		raw := m[1]
		if amountKnown(raw, allowed.Amounts) {
			resolved++
			continue
		}
		report.UnverifiedAmounts = append(report.UnverifiedAmounts, "$"+raw)
	}

	if len(report.UnknownResources) > 0 || len(report.UnverifiedAmounts) > 0 {
		report.Grounded = false
		for _, r := range report.UnknownResources {
			report.Issues = append(report.Issues, "resource/account reference not found in this turn's tool results: "+r)
		}
		for _, a := range report.UnverifiedAmounts {
			report.Issues = append(report.Issues, "dollar figure not traceable to any tool result: "+a)
		}
	}
	if checkable == 0 {
		report.Confidence = 1
	} else {
		report.Confidence = float64(resolved) / float64(checkable)
	}
	return report, nil
}

func resourceKnown(token string, allowed ports.GroundingSet) bool {
	if _, ok := allowed.ResourceIDs[token]; ok {
		return true
	}
	if allowed.ResourceNames[token] {
		return true
	}
	if allowed.Recommendations[token] {
		return true
	}
	if allowed.Applications[token] {
		return true
	}
	if allowed.Transactions[token] {
		return true
	}
	return false
}

// amountKnown reports whether a dollar figure parsed from the answer
// matches one of the amounts the tool results actually produced, to the
// nearest whole dollar (the copilot's money() helper renders anything over
// $100 without cents, so the answer text itself never carries more
// precision than that once formatted).
func amountKnown(raw string, allowed []core.Money) bool {
	cleaned := strings.ReplaceAll(raw, ",", "")
	f, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return false
	}
	want := int64(f + 0.5)
	for _, m := range allowed {
		got := int64(m.Units() + 0.5)
		if got == want {
			return true
		}
		// Also accept a match against the absolute value, since an answer
		// may state a delta ("down $500") where the tool result carried the
		// signed figure.
		if got == -want {
			return true
		}
	}
	return false
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
