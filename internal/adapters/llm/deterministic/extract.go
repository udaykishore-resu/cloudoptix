package deterministic

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// extractor pulls one field's value out of free text, reporting whether it
// found positive evidence. Returning found=false is what lets the caller
// (internal/application/onboarding) tell "the model looked and found
// nothing" apart from "the model found an empty string", exactly like a real
// structured-output call would simply omit a key it had no basis for.
type extractor func(text string) (any, bool)

// registry maps the canonical property names CloudOptix's onboarding
// extraction schema uses (see internal/application/onboarding/schema.go) to
// the extractor that fills them. Keeping this as a name-keyed table rather
// than a struct is what lets Extract handle whatever subset of properties a
// given ResponseSchema actually asks for, generically.
var registry = map[string]extractor{
	"organization_name":            extractOrganizationName,
	"industry":                     extractIndustry,
	"company_size":                 extractCompanySize,
	"business_regions":             extractBusinessRegions,
	"application_name":             extractApplicationName,
	"application_description":      extractApplicationDescription,
	"domain":                       extractDomain,
	"architecture_style":           extractArchitectureStyle,
	"compute_platforms":            extractComputePlatforms,
	"databases":                    extractDatabases,
	"caches":                       extractCaches,
	"messaging":                    extractMessaging,
	"aws_account_ids":              extractAWSAccountIDs,
	"aws_regions":                  extractAWSRegions,
	"environments":                 extractEnvironments,
	"business_transactions":        extractBusinessTransactions,
	"cost_reduction_target":        extractCostReductionTarget,
	"monthly_budget":               extractMonthlyBudget,
	"availability_target":          extractAvailabilityTarget,
	"max_latency_ms":               extractMaxLatencyMS,
	"risk_tolerance":               extractRiskTolerance,
	"spot_allowed":                 extractSpotAllowed,
	"automation_enabled":           extractAutomationEnabled,
	"governance_requires_approval": extractGovernanceRequiresApproval,
	"compliance_frameworks":        extractComplianceFrameworks,
}

// Extract walks the "properties" of a JSON Schema object and, for every
// property name this package recognises, applies its extractor to the
// concatenated content of every RoleUser message in msgs. Properties it does
// not recognise, or for which no extractor found evidence, are simply absent
// from the result — never filled with a zero value, which would be
// indistinguishable from a confirmed empty answer.
func Extract(schema map[string]any, msgs []ports.Message) map[string]any {
	text := userText(msgs)
	out := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return out
	}
	props, _ := schema["properties"].(map[string]any)
	for name := range props {
		ext, ok := registry[name]
		if !ok {
			continue
		}
		if v, found := ext(text); found {
			out[name] = v
		}
	}
	return out
}

func userText(msgs []ports.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == ports.RoleUser {
			if b.Len() > 0 {
				b.WriteString(" ")
			}
			b.WriteString(m.Content)
		}
	}
	return b.String()
}

var (
	// The cue phrase is matched case-insensitively via a scoped (?i:...)
	// group; the captured name itself deliberately stays case-sensitive
	// (plain [A-Z]) outside that group; folding case over the whole pattern
	// would make [A-Z] match lowercase too and swallow the rest of the
	// sentence into the "name".
	orgNameRe   = regexp.MustCompile(`(?i:\b(?:we are|we're|our company is|company called|company is|called))\s+([A-Z][A-Za-z0-9&.'-]*(?:\s+[A-Z][A-Za-z0-9&.'-]*){0,3})`)
	appNameRe   = regexp.MustCompile(`(?i:\b(?:platform (?:is )?called|application (?:is )?called|product (?:is )?called|app (?:is )?called|it'?s called))\s+([A-Z][A-Za-z0-9&.'-]*(?:\s+[A-Z][A-Za-z0-9&.'-]*){0,3})`)
	accountIDRe = regexp.MustCompile(`\b\d{12}\b`)
	regionRe    = regexp.MustCompile(`\b[a-z]{2}(?:-gov)?-(?:north|south|east|west|central|northeast|southeast|northwest|southwest)?-?\d\b`)
	pctRe       = regexp.MustCompile(`(\d{1,3}(?:\.\d+)?)\s*%`)
	moneyRe     = regexp.MustCompile(`\$\s?([\d,]+(?:\.\d+)?)\s*(k|K|thousand|m|M|million)?`)
	latencyRe   = regexp.MustCompile(`(\d+)\s*ms\b`)
	volumeRe    = regexp.MustCompile(`(?i)([\d,]+(?:\.\d+)?)\s*(k|thousand|m|million)?\s*(checkouts?|orders?|transactions?|payments?|claims?|searches?|requests?|logins?)\s*(?:per|/)\s*month`)
)

// trimName cleans a captured proper noun.
//
// The sentence-boundary cut is the load-bearing part. The name patterns
// allow "." inside a captured word (so "Acme Corp." and "F.C. Retail"
// survive) and allow up to three following capitalised words (so
// "Northfield Commerce Group" survives). Together those two allowances let a
// capture run straight past a full stop into the next sentence: "our
// platform is called PayFlow. We operate..." captured "PayFlow. We". Cutting
// at ". " — a period followed by whitespace, which no real name contains
// mid-word — keeps both allowances and removes the overrun.
func trimName(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, ". "); idx > 0 {
		s = s[:idx]
	}
	s = strings.TrimRight(s, ".,;:")
	return s
}

func extractOrganizationName(text string) (any, bool) {
	if m := orgNameRe.FindStringSubmatch(text); len(m) == 2 {
		return trimName(m[1]), true
	}
	return nil, false
}

func extractApplicationName(text string) (any, bool) {
	if m := appNameRe.FindStringSubmatch(text); len(m) == 2 {
		return trimName(m[1]), true
	}
	return nil, false
}

func extractApplicationDescription(text string) (any, bool) {
	// Weak but real signal: a sentence containing "we run" / "we operate" /
	// "our platform" is treated as a description candidate. This is
	// deliberately conservative — it only fires on an explicit
	// self-description cue rather than guessing from unrelated prose.
	lower := strings.ToLower(text)
	for _, cue := range []string{"we run ", "we operate ", "our platform ", "our application ", "we build "} {
		if idx := strings.Index(lower, cue); idx >= 0 {
			rest := text[idx:]
			if end := strings.IndexAny(rest, ".!\n"); end > 0 {
				rest = rest[:end]
			}
			rest = trimName(rest)
			if len(rest) > 8 {
				return rest, true
			}
		}
	}
	return nil, false
}

type keywordMap struct {
	canonical string
	keywords  []string
}

func matchAny(text string, table []keywordMap) []string {
	lower := strings.ToLower(text)
	var out []string
	seen := map[string]bool{}
	for _, km := range table {
		for _, kw := range km.keywords {
			if strings.Contains(lower, kw) {
				if !seen[km.canonical] {
					seen[km.canonical] = true
					out = append(out, km.canonical)
				}
				break
			}
		}
	}
	return out
}

var industryTable = []keywordMap{
	{"e-commerce", []string{"e-commerce", "ecommerce", "online retail", "online store"}},
	{"retail", []string{"retail", "retailer"}},
	{"healthcare", []string{"healthcare", "health care", "clinical", "hospital", "patient"}},
	{"financial_services", []string{"fintech", "banking", "bank", "financial services", "payments company", "lending"}},
	{"insurance", []string{"insurance", "insurer", "claims"}},
	{"saas", []string{"saas", "software as a service", "b2b software"}},
	{"media", []string{"media company", "streaming", "publishing"}},
	{"gaming", []string{"gaming", "game studio", "video game"}},
	{"logistics", []string{"logistics", "shipping", "supply chain", "freight"}},
	{"manufacturing", []string{"manufacturing", "manufacturer", "factory"}},
	{"education", []string{"education", "edtech", "university", "school"}},
	{"travel", []string{"travel", "airline", "hospitality", "booking platform"}},
	{"telecommunications", []string{"telecom", "telecommunications"}},
}

func extractIndustry(text string) (any, bool) {
	m := matchAny(text, industryTable)
	if len(m) == 0 {
		return nil, false
	}
	return m[0], true
}

var domainTable = []keywordMap{
	{"checkout", []string{"checkout", "cart", "purchase flow"}},
	{"payments", []string{"payment processing", "payments platform"}},
	{"claims", []string{"claims processing", "claims"}},
	{"search", []string{"search platform", "search engine"}},
	{"booking", []string{"booking", "reservations"}},
	{"logistics", []string{"fulfillment", "delivery tracking"}},
	{"content", []string{"content delivery", "streaming platform"}},
}

func extractDomain(text string) (any, bool) {
	m := matchAny(text, domainTable)
	if len(m) == 0 {
		return nil, false
	}
	return m[0], true
}

var sizeTable = []keywordMap{
	{"startup", []string{"startup", "early stage", "seed stage"}},
	{"small", []string{"small company", "small business", "small team"}},
	{"midmarket", []string{"mid-size", "midsize", "mid market", "growth stage"}},
	{"enterprise", []string{"enterprise", "large company", "fortune 500", "global company"}},
}

func extractCompanySize(text string) (any, bool) {
	m := matchAny(text, sizeTable)
	if len(m) == 0 {
		return nil, false
	}
	return m[0], true
}

var regionNameTable = []keywordMap{
	{"north_america", []string{"north america", "united states", "u.s.", "usa", "canada"}},
	{"europe", []string{"europe", "eu ", "european union"}},
	{"apac", []string{"apac", "asia pacific", "asia-pacific"}},
	{"latam", []string{"latin america", "latam"}},
}

func extractBusinessRegions(text string) (any, bool) {
	m := matchAny(text, regionNameTable)
	if len(m) == 0 {
		return nil, false
	}
	return m, true
}

var architectureStyleTable = []keywordMap{
	{"microservices", []string{"microservices", "micro-services", "micro services"}},
	{"monolith", []string{"monolith", "monolithic"}},
	{"serverless", []string{"serverless", "fully serverless"}},
}

func extractArchitectureStyle(text string) (any, bool) {
	m := matchAny(text, architectureStyleTable)
	if len(m) == 0 {
		return nil, false
	}
	return m[0], true
}

var computePlatformTable = []keywordMap{
	{"eks", []string{"eks", "kubernetes", "k8s"}},
	{"ecs", []string{"ecs"}},
	{"fargate", []string{"fargate"}},
	{"lambda", []string{"lambda", "serverless functions"}},
	{"ec2", []string{"ec2", "virtual machines", "vms"}},
}

func extractComputePlatforms(text string) (any, bool) {
	m := matchAny(text, computePlatformTable)
	if len(m) == 0 {
		return nil, false
	}
	return m, true
}

var databaseTable = []keywordMap{
	{"postgresql", []string{"postgres", "postgresql"}},
	{"mysql", []string{"mysql", "aurora mysql"}},
	{"dynamodb", []string{"dynamodb", "dynamo db"}},
	{"aurora", []string{"aurora postgres", "aurora"}},
	{"mongodb", []string{"mongodb", "mongo db", "documentdb"}},
}

func extractDatabases(text string) (any, bool) {
	m := matchAny(text, databaseTable)
	if len(m) == 0 {
		return nil, false
	}
	return m, true
}

var cacheTable = []keywordMap{
	{"redis", []string{"redis"}},
	{"memcached", []string{"memcached"}},
	{"elasticache", []string{"elasticache"}},
}

func extractCaches(text string) (any, bool) {
	m := matchAny(text, cacheTable)
	if len(m) == 0 {
		return nil, false
	}
	return m, true
}

var messagingTable = []keywordMap{
	{"sqs", []string{"sqs"}},
	{"sns", []string{"sns"}},
	{"kafka", []string{"kafka", "msk"}},
	{"kinesis", []string{"kinesis"}},
	{"eventbridge", []string{"eventbridge", "event bus"}},
}

func extractMessaging(text string) (any, bool) {
	m := matchAny(text, messagingTable)
	if len(m) == 0 {
		return nil, false
	}
	return m, true
}

func extractAWSAccountIDs(text string) (any, bool) {
	m := accountIDRe.FindAllString(text, -1)
	if len(m) == 0 {
		return nil, false
	}
	return m, true
}

func extractAWSRegions(text string) (any, bool) {
	m := regionRe.FindAllString(text, -1)
	if len(m) == 0 {
		return nil, false
	}
	return dedupe(m), true
}

var environmentTable = []keywordMap{
	{"production", []string{"production", "prod "}},
	{"staging", []string{"staging", "stage "}},
	{"development", []string{"development", "dev "}},
	{"test", []string{"testing", "test env"}},
	{"sandbox", []string{"sandbox"}},
}

func extractEnvironments(text string) (any, bool) {
	m := matchAny(text, environmentTable)
	if len(m) == 0 {
		return nil, false
	}
	return m, true
}

func extractBusinessTransactions(text string) (any, bool) {
	matches := volumeRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, false
	}
	var out []map[string]any
	for _, m := range matches {
		vol, ok := parseScaledNumber(m[1], m[2])
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"name":           normalizeTransactionNoun(m[3]),
			"monthly_volume": vol,
		})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func normalizeTransactionNoun(noun string) string {
	n := strings.ToLower(strings.TrimSuffix(noun, "s"))
	return n
}

func parseScaledNumber(numStr, scale string) (float64, bool) {
	numStr = strings.ReplaceAll(numStr, ",", "")
	n, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(scale) {
	case "k", "thousand":
		n *= 1000
	case "m", "million":
		n *= 1_000_000
	}
	return n, true
}

// cueWindowAfter bounds how far past a cue word this package searches for
// the number that belongs to it. A message can state several unrelated
// figures in one sentence ("cut cost by 25%... our availability target is
// 99.95%"); searching the whole text for "any percentage" would attribute
// the wrong number to the wrong field the moment more than one appears.
// windowNear therefore looks forward only, starting immediately after the
// matched cue phrase — CloudOptix's own phrasing conventions, and ordinary
// English, put the value after the cue ("availability target is 99.95%",
// "cut spend by 25%") — so an unrelated number stated earlier in the same
// sentence never leaks into the window at all.
const cueWindowAfter = 36

// windowNear returns the substring of text starting right after the first
// occurrence of any word in cues (case-insensitive) and extending up to
// cueWindowAfter characters forward, or "", false if none of them appear.
func windowNear(text string, cues []string) (string, bool) {
	lower := strings.ToLower(text)
	best, bestLen := -1, 0
	for _, c := range cues {
		if idx := strings.Index(lower, c); idx >= 0 && (best == -1 || idx < best) {
			best, bestLen = idx, len(c)
		}
	}
	if best == -1 {
		return "", false
	}
	start := best + bestLen
	end := start + cueWindowAfter
	if end > len(text) {
		end = len(text)
	}
	return text[start:end], true
}

func extractCostReductionTarget(text string) (any, bool) {
	window, ok := windowNear(text, []string{"cut", "reduce", "save", "lower", "trim"})
	if !ok {
		return nil, false
	}
	m := pctRe.FindStringSubmatch(window)
	if m == nil {
		return nil, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil, false
	}
	return v / 100, true
}

func extractMonthlyBudget(text string) (any, bool) {
	window, ok := windowNear(text, []string{"budget"})
	if !ok {
		return nil, false
	}
	m := moneyRe.FindStringSubmatch(window)
	if m == nil {
		return nil, false
	}
	v, ok := parseScaledNumber(m[1], m[2])
	if !ok {
		return nil, false
	}
	return v, true
}

func extractAvailabilityTarget(text string) (any, bool) {
	window, ok := windowNear(text, []string{"availability", "uptime", "sla"})
	if !ok {
		return nil, false
	}
	m := pctRe.FindStringSubmatch(window)
	if m == nil {
		return nil, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || v <= 0 || v > 100 {
		return nil, false
	}
	return v / 100, true
}

func extractMaxLatencyMS(text string) (any, bool) {
	window, ok := windowNear(text, []string{"latency"})
	if !ok {
		return nil, false
	}
	m := latencyRe.FindStringSubmatch(window)
	if m == nil {
		return nil, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil, false
	}
	return v, true
}

func extractRiskTolerance(text string) (any, bool) {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "risk") {
		return nil, false
	}
	switch {
	case strings.Contains(lower, "low risk") || strings.Contains(lower, "conservative") || strings.Contains(lower, "cautious"):
		return "low", true
	case strings.Contains(lower, "high risk") || strings.Contains(lower, "aggressive"):
		return "high", true
	case strings.Contains(lower, "medium risk") || strings.Contains(lower, "moderate"):
		return "medium", true
	}
	return nil, false
}

func extractSpotAllowed(text string) (any, bool) {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "spot") {
		return nil, false
	}
	if negatedNear(lower, "spot") {
		return false, true
	}
	return true, true
}

func extractAutomationEnabled(text string) (any, bool) {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "automat") {
		return nil, false
	}
	if negatedNear(lower, "automat") {
		return false, true
	}
	return true, true
}

func extractGovernanceRequiresApproval(text string) (any, bool) {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "require approval"), strings.Contains(lower, "need approval"),
		strings.Contains(lower, "sign off"), strings.Contains(lower, "human approval"):
		return true, true
	case strings.Contains(lower, "no approval needed"), strings.Contains(lower, "fully automatic"),
		strings.Contains(lower, "without approval"):
		return false, true
	}
	return nil, false
}

var complianceTable = []keywordMap{
	{"SOC2", []string{"soc 2", "soc2"}},
	{"HIPAA", []string{"hipaa"}},
	{"PCI-DSS", []string{"pci-dss", "pci dss", "pci compliance"}},
	{"GDPR", []string{"gdpr"}},
	{"ISO27001", []string{"iso 27001", "iso27001"}},
	{"FedRAMP", []string{"fedramp"}},
}

func extractComplianceFrameworks(text string) (any, bool) {
	m := matchAny(text, complianceTable)
	if len(m) == 0 {
		return nil, false
	}
	return m, true
}

// negatedNear reports whether a common negation word appears within a small
// window before the cue phrase — "no spot instances", "don't want
// automation" — a cheap but effective sentiment flip for the yes/no fields.
func negatedNear(lower, cue string) bool {
	idx := strings.Index(lower, cue)
	if idx < 0 {
		return false
	}
	start := idx - 25
	if start < 0 {
		start = 0
	}
	window := lower[start:idx]
	for _, neg := range []string{"no ", "not ", "n't ", "don't", "without", "avoid"} {
		if strings.Contains(window, neg) {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
