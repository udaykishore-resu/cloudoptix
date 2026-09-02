package deterministic

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// toolTriggers maps every tool CloudOptix's copilot registers to the phrases
// that route a question to it. This table is what lets a fixed, offline
// provider answer the specific questions the product promises — "why did
// AWS cost increase", "what is wasting money", "which service is most
// expensive", "how do we cut 30%", "what is our cost per transaction",
// "which architecture is cheapest", "what happens if traffic doubles",
// "which service has the highest economic blast radius", "what Terraform
// change increased cost", "what should we optimize first" — each maps to at
// least one entry here.
var toolTriggers = map[string][]string{
	"get_cost_summary":         {"cost summary", "how much are we spending", "total cost", "overview of cost", "monthly spend", "how much did we spend"},
	"get_cost_breakdown":       {"most expensive", "breakdown", "by service", "which service costs", "cost by"},
	"explain_cost_change":      {"why did", "cost increase", "cost went up", "cost changed", "terraform", "what changed", "cost regression"},
	"list_resources":           {"list resources", "show resources", "what resources", "which resources"},
	"get_resource":             {"resource id", "this resource", "specific instance"},
	"get_economic_footprint":   {"economic footprint", "footprint of"},
	"get_unit_economics":       {"cost per transaction", "unit economics", "per checkout", "per order", "per customer", "cost per unit"},
	"list_recommendations":     {"recommendations", "what should we optimize", "optimize first", "wasting money", "waste", "what's wasting"},
	"get_recommendation":       {"recommendation id", "rec_", "that recommendation"},
	"get_efficiency_score":     {"efficiency score", "how efficient", "cloud efficiency"},
	"get_cost_slo_status":      {"slo status", "error budget", "cost slo", "budget breach"},
	"get_savings_funnel":       {"savings funnel", "cut 30", "cut cost by", "reduce cost by", "how do we cut", "how do we save"},
	"query_architecture_graph": {"architecture", "cheapest architecture", "architecture graph", "dependency graph"},
	"get_blast_radius":         {"blast radius", "highest economic blast", "biggest impact if"},
	"run_counterfactual":       {"what if", "traffic doubles", "what happens if", "counterfactual", "scenario"},
	"search_knowledge":         {"what is", "explain", "how does", "why is", "best practice"},
}

// toolMatch is one candidate tool with its relevance score.
type toolMatch struct {
	name  string
	score int
}

// matchTools ranks the offered tools by how many trigger phrases from
// toolTriggers appear in question, restricted to tools actually present in
// available (the copilot only ever offers the fixed read-only registry, but
// this keeps the matcher honest against whatever set it is actually given).
// Ties keep the order available lists them in, so the copilot's own
// registration order — cost summary before narrower tools — acts as the
// tie-break, mirroring how a real model tends to reach for the most general
// applicable tool first.
func matchTools(question string, available []ports.ToolDefinition) []toolMatch {
	lower := strings.ToLower(question)
	allowed := make(map[string]bool, len(available))
	order := make(map[string]int, len(available))
	for i, t := range available {
		allowed[t.Name] = true
		order[t.Name] = i
	}
	var matches []toolMatch
	for name, triggers := range toolTriggers {
		if !allowed[name] {
			continue
		}
		score := 0
		for _, trig := range triggers {
			if strings.Contains(lower, trig) {
				score++
			}
		}
		if score > 0 {
			matches = append(matches, toolMatch{name: name, score: score})
		}
	}
	sortToolMatches(matches, order)
	return matches
}

func sortToolMatches(matches []toolMatch, order map[string]int) {
	for i := 1; i < len(matches); i++ {
		j := i
		for j > 0 {
			better := matches[j].score > matches[j-1].score ||
				(matches[j].score == matches[j-1].score && order[matches[j].name] < order[matches[j-1].name])
			if !better {
				break
			}
			matches[j], matches[j-1] = matches[j-1], matches[j]
			j--
		}
	}
}

var (
	resourceIDRe  = regexp.MustCompile(`\b(?:i-|vol-|snap-|ami-|sg-|vpc-|subnet-|nat-|rec_|res_)[0-9a-zA-Z]{4,}\b`)
	multiplierRe  = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*x\b`)
	percentBumpRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%`)
)

// buildArgs infers a reasonable argument map for the chosen tool from the
// question text. Every tool implementation must treat these as optional
// hints and fall back to sensible defaults (a trailing-30-day period, no
// resource filter) when a hint is absent — the deterministic provider is
// intentionally conservative about guessing rather than fabricating a
// specific identifier it has no evidence for.
func buildArgs(toolName, question string) map[string]any {
	lower := strings.ToLower(question)
	args := map[string]any{}

	switch toolName {
	case "get_cost_breakdown":
		args["dimension"] = "service"
	case "get_resource", "get_recommendation":
		if id := resourceIDRe.FindString(question); id != "" {
			args["id"] = id
		}
	case "list_recommendations":
		if strings.Contains(lower, "waste") || strings.Contains(lower, "wasting") {
			args["category"] = "waste"
		}
	case "run_counterfactual":
		args["scenario_type"] = "traffic_change"
		multiplier := 1.5
		if strings.Contains(lower, "double") {
			multiplier = 2.0
		} else if strings.Contains(lower, "triple") {
			multiplier = 3.0
		} else if strings.Contains(lower, "half") {
			multiplier = 0.5
		} else if m := multiplierRe.FindStringSubmatch(lower); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				multiplier = v
			}
		} else if m := percentBumpRe.FindStringSubmatch(lower); m != nil && strings.Contains(lower, "increas") {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				multiplier = 1 + v/100
			}
		}
		args["multiplier"] = multiplier
	case "get_savings_funnel":
		if m := percentBumpRe.FindStringSubmatch(lower); m != nil {
			args["target_pct"] = m[1]
		}
	case "get_blast_radius":
		if id := resourceIDRe.FindString(question); id != "" {
			args["resource_id"] = id
		}
	case "search_knowledge":
		args["query"] = question
	}
	return args
}
